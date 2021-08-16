package graph

import (
	"context"
	"fmt"

	v0 "github.com/authzed/authzed-go/proto/authzed/api/v0"
	"github.com/rs/zerolog/log"

	"github.com/authzed/spicedb/internal/datastore"
	"github.com/authzed/spicedb/internal/namespace"
	"github.com/authzed/spicedb/pkg/tuple"
)

func newConcurrentLookup(d Dispatcher, ds datastore.GraphDatastore, nsm namespace.Manager) lookupHandler {
	return &concurrentLookup{d: d, ds: ds, nsm: nsm}
}

type concurrentLookup struct {
	d   Dispatcher
	ds  datastore.GraphDatastore
	nsm namespace.Manager
}

// Calculate the maximum int value to allow us to effectively set no limit on certain recursive
// lookup calls.
const (
	maxUint = ^uint(0)
	noLimit = int(maxUint >> 1)
)

func (cl *concurrentLookup) lookup(ctx context.Context, req LookupRequest) ReduceableLookupFunc {
	log.Trace().Object("lookup", req).Send()

	objSet := tuple.NewONRSet()

	// If we've found the target ONR, add it to the set of resolved objects. Note that we still need
	// to continue processing, as this may also be an intermediate step in resolution.
	if req.StartRelation.Namespace == req.TargetONR.Namespace && req.StartRelation.Relation == req.TargetONR.Relation {
		objSet.Add(req.TargetONR)
		req.DebugTracer.Child("(self)")
	}

	nsdef, typeSystem, _, err := cl.nsm.ReadNamespaceAndTypes(ctx, req.StartRelation.Namespace)
	if err != nil {
		return ResolveError(err)
	}

	relation, ok := findRelation(nsdef, req.StartRelation.Relation)
	if !ok {
		return ResolveError(fmt.Errorf("relation `%s` not found under namespace `%s`", req.StartRelation.Relation, req.StartRelation.Namespace))
	}

	rewrite := relation.UsersetRewrite
	var request ReduceableLookupFunc
	if rewrite != nil {
		request = cl.processRewrite(ctx, req, req.DebugTracer, nsdef, typeSystem, rewrite)
	} else {
		request = cl.lookupDirect(ctx, req, req.DebugTracer, typeSystem)
	}

	// Perform the structural lookup.
	result := LookupAny(ctx, req.Limit, []ReduceableLookupFunc{request})
	if result.Err != nil {
		return ResolveError(result.Err)
	}

	objSet.Update(result.ResolvedObjects)

	// Recursively perform lookup on any of the ONRs found that do not match the target ONR.
	// This ensures that we resolve the full transitive closure of all objects.
	recursiveTracer := req.DebugTracer.Child("Recursive")
	toCheck := objSet
	for {
		if toCheck.Length() == 0 || objSet.Length() >= req.Limit {
			break
		}

		loopTracer := recursiveTracer.Child("Loop")
		outgoingTracer := loopTracer.Child("Outgoing")

		var requests []ReduceableLookupFunc
		for _, obj := range toCheck.AsSlice() {
			// If we've already found the target ONR, no further resolution is necessary.
			if obj.Namespace == req.TargetONR.Namespace &&
				obj.Relation == req.TargetONR.Relation &&
				obj.ObjectId == req.TargetONR.ObjectId {
				continue
			}

			requests = append(requests, cl.dispatch(LookupRequest{
				TargetONR:      obj,
				StartRelation:  req.StartRelation,
				Limit:          req.Limit - objSet.Length(),
				AtRevision:     req.AtRevision,
				DepthRemaining: req.DepthRemaining - 1,
				DirectStack:    req.DirectStack,
				TTUStack:       req.TTUStack,
				DebugTracer:    outgoingTracer.Childf("%s", tuple.StringONR(obj)),
			}))
		}

		if len(requests) == 0 {
			break
		}

		result := LookupAny(ctx, req.Limit, requests)
		if result.Err != nil {
			return ResolveError(err)
		}

		toCheck = tuple.NewONRSet()

		resultsTracer := loopTracer.Child("Results")
		for _, obj := range result.ResolvedObjects {
			resultsTracer.ChildONR(obj)

			// Only check recursively for new objects.
			if objSet.Add(obj) {
				toCheck.Add(obj)
			}
		}
	}

	return ResolvedObjects(limitedSlice(objSet.AsSlice(), req.Limit))
}

func (cl *concurrentLookup) lookupDirect(ctx context.Context, req LookupRequest, tracer DebugTracer, typeSystem *namespace.NamespaceTypeSystem) ReduceableLookupFunc {
	requests := []ReduceableLookupFunc{}
	thisTracer := tracer.Child("_this")

	// Dispatch a check for the target ONR directly, if it is allowed on the start relation.
	isDirectAllowed, err := typeSystem.IsAllowedDirectRelation(req.StartRelation.Relation, req.TargetONR.Namespace, req.TargetONR.Relation)
	if err != nil {
		return ResolveError(err)
	}

	if isDirectAllowed == namespace.DirectRelationValid {
		requests = append(requests, func(ctx context.Context, resultChan chan<- LookupResult) {
			objects := tuple.NewONRSet()
			it, err := cl.ds.ReverseQueryTuplesFromSubject(req.TargetONR, req.AtRevision).
				WithObjectRelation(req.StartRelation.Namespace, req.StartRelation.Relation).
				Execute(ctx)
			if err != nil {
				resultChan <- LookupResult{Err: err}
				return
			}
			defer it.Close()

			for tpl := it.Next(); tpl != nil; tpl = it.Next() {
				if it.Err() != nil {
					resultChan <- LookupResult{Err: it.Err()}
					return
				}

				objects.Add(tpl.ObjectAndRelation)
				if objects.Length() >= req.Limit {
					break
				}
			}

			if it.Err() != nil {
				resultChan <- LookupResult{Err: err}
				return
			}

			thisTracer.Add("Local", EmittableObjectSet(*objects))
			resultChan <- LookupResult{ResolvedObjects: objects.AsSlice()}
			return
		})
	}

	// Dispatch to any allowed direct relation types that don't match the target ONR, collect
	// the found object IDs, and then search for those.
	allowedDirect, err := typeSystem.AllowedDirectRelations(req.StartRelation.Relation)
	if err != nil {
		return ResolveError(err)
	}

	directTracer := thisTracer.Child("Inferred")
	requestsTracer := directTracer.Child("Requests")

	directStack := req.DirectStack.With(&v0.ObjectAndRelation{
		Namespace: req.StartRelation.Namespace,
		Relation:  req.StartRelation.Relation,
		ObjectId:  "",
	})

	for _, allowedDirectType := range allowedDirect {
		if allowedDirectType.Relation == Ellipsis {
			continue
		}

		if allowedDirectType.Namespace == req.StartRelation.Namespace &&
			allowedDirectType.Relation == req.StartRelation.Relation {
			continue
		}

		// Prevent recursive inferred lookups, which can cause an infinite loop.
		onr := &v0.ObjectAndRelation{
			Namespace: allowedDirectType.Namespace,
			Relation:  allowedDirectType.Relation,
			ObjectId:  "",
		}
		if directStack.Has(onr) {
			requestsTracer.Childf("Skipping %s", tuple.StringONR(onr))
			continue
		}

		// Bind to the current allowed direct type
		allowedDirectType := allowedDirectType

		requests = append(requests, func(ctx context.Context, resultChan chan<- LookupResult) {
			// Dispatch on the inferred relation.
			inferredRequest := cl.dispatch(LookupRequest{
				TargetONR: req.TargetONR,
				StartRelation: &v0.RelationReference{
					Namespace: allowedDirectType.Namespace,
					Relation:  allowedDirectType.Relation,
				},
				Limit:          noLimit, // Since this is an inferred lookup, we can't limit.
				AtRevision:     req.AtRevision,
				DepthRemaining: req.DepthRemaining - 1,
				DirectStack:    directStack,
				TTUStack:       req.TTUStack,
				DebugTracer:    requestsTracer.Childf("Incoming %s#%s", allowedDirectType.Namespace, allowedDirectType.Relation),
			})

			result := LookupAny(ctx, noLimit, []ReduceableLookupFunc{inferredRequest})
			if result.Err != nil {
				resultChan <- result
				return
			}

			// For each inferred object found, check for the target ONR.
			resultsTracer := directTracer.Child("Results To Check")
			objects := tuple.NewONRSet()
			if len(result.ResolvedObjects) > 0 {
				it, err := cl.ds.QueryTuples(req.StartRelation.Namespace, req.AtRevision).
					WithRelation(req.StartRelation.Relation).
					WithUsersets(result.ResolvedObjects).
					Limit(uint64(req.Limit)).
					Execute(ctx)
				if err != nil {
					resultChan <- LookupResult{Err: err}
					return
				}
				defer it.Close()

				for tpl := it.Next(); tpl != nil; tpl = it.Next() {
					if it.Err() != nil {
						resultChan <- LookupResult{Err: it.Err()}
						return
					}

					resultsTracer.Child(tuple.StringONR(tpl.ObjectAndRelation))

					objects.Add(tpl.ObjectAndRelation)
					if objects.Length() >= req.Limit {
						break
					}
				}
			}

			resultChan <- LookupResult{ResolvedObjects: objects.AsSlice()}
		})
	}

	if len(requests) == 0 {
		return ResolvedObjects([]*v0.ObjectAndRelation{})
	}

	return func(ctx context.Context, resultChan chan<- LookupResult) {
		resultChan <- LookupAny(ctx, req.Limit, requests)
	}
}

func (cl *concurrentLookup) processRewrite(ctx context.Context, req LookupRequest, tracer DebugTracer, nsdef *v0.NamespaceDefinition, typeSystem *namespace.NamespaceTypeSystem, usr *v0.UsersetRewrite) ReduceableLookupFunc {
	switch rw := usr.RewriteOperation.(type) {
	case *v0.UsersetRewrite_Union:
		return cl.processSetOperation(ctx, req, tracer.Child("union"), nsdef, typeSystem, rw.Union, LookupAny)
	case *v0.UsersetRewrite_Intersection:
		return cl.processSetOperation(ctx, req, tracer.Child("intersection"), nsdef, typeSystem, rw.Intersection, LookupAll)
	case *v0.UsersetRewrite_Exclusion:
		return cl.processSetOperation(ctx, req, tracer.Child("exclusion"), nsdef, typeSystem, rw.Exclusion, LookupExclude)
	default:
		return ResolveError(fmt.Errorf("unknown userset rewrite kind under `%s#%s`", req.StartRelation.Namespace, req.StartRelation.Relation))
	}
}

func (cl *concurrentLookup) processSetOperation(ctx context.Context, req LookupRequest, parentTracer DebugTracer, nsdef *v0.NamespaceDefinition, typeSystem *namespace.NamespaceTypeSystem, so *v0.SetOperation, reducer LookupReducer) ReduceableLookupFunc {
	var requests []ReduceableLookupFunc

	tracer := parentTracer.Child("rewrite")
	for _, childOneof := range so.Child {
		switch child := childOneof.ChildType.(type) {
		case *v0.SetOperation_Child_XThis:
			requests = append(requests, cl.lookupDirect(ctx, req, tracer, typeSystem))
		case *v0.SetOperation_Child_ComputedUserset:
			requests = append(requests, cl.lookupComputed(ctx, req, tracer, child.ComputedUserset))
		case *v0.SetOperation_Child_UsersetRewrite:
			requests = append(requests, cl.processRewrite(ctx, req, tracer, nsdef, typeSystem, child.UsersetRewrite))
		case *v0.SetOperation_Child_TupleToUserset:
			requests = append(requests, cl.processTupleToUserset(ctx, req, tracer, nsdef, typeSystem, child.TupleToUserset))
		default:
			return ResolveError(fmt.Errorf("unknown set operation child"))
		}
	}
	return func(ctx context.Context, resultChan chan<- LookupResult) {
		log.Trace().Object("set operation", req).Stringer("operation", so).Send()
		resultChan <- reducer(ctx, req.Limit, requests)
	}
}

func findRelation(nsdef *v0.NamespaceDefinition, relationName string) (*v0.Relation, bool) {
	for _, relation := range nsdef.Relation {
		if relation.Name == relationName {
			return relation, true
		}
	}

	return nil, false
}

func (cl *concurrentLookup) processTupleToUserset(ctx context.Context, req LookupRequest, tracer DebugTracer, nsdef *v0.NamespaceDefinition, typeSystem *namespace.NamespaceTypeSystem, ttu *v0.TupleToUserset) ReduceableLookupFunc {
	// Ensure that we don't process TTUs recursively, as that can cause an infinite loop.
	onr := &v0.ObjectAndRelation{
		Namespace: req.StartRelation.Namespace,
		Relation:  req.StartRelation.Relation,
		ObjectId:  "",
	}
	if req.TTUStack.Has(onr) {
		tracer.Childf("recursive ttu %s#%s", req.StartRelation.Namespace, req.StartRelation.Relation)
		return ResolvedObjects([]*v0.ObjectAndRelation{})
	}

	tuplesetDirectRelations, err := typeSystem.AllowedDirectRelations(ttu.Tupleset.Relation)
	if err != nil {
		return ResolveError(err)
	}

	ttuTracer := tracer.Childf("ttu %s#%s <- %s", req.StartRelation.Namespace, ttu.Tupleset, ttu.ComputedUserset.Relation)

	// Dispatch to all the accessible namespaces for the computed userset.
	requests := []ReduceableLookupFunc{}
	namespaces := map[string]bool{}

	computedUsersetTracer := ttuTracer.Child("computed_userset")
	for _, directRelation := range tuplesetDirectRelations {
		_, ok := namespaces[directRelation.Namespace]
		if ok {
			continue
		}

		_, directRelTypeSystem, _, err := cl.nsm.ReadNamespaceAndTypes(ctx, directRelation.Namespace)
		if err != nil {
			return ResolveError(err)
		}

		if !directRelTypeSystem.HasRelation(ttu.ComputedUserset.Relation) {
			continue
		}

		namespaces[directRelation.Namespace] = true

		// Bind the current direct relation.
		directRelation := directRelation

		requests = append(requests, func(ctx context.Context, resultChan chan<- LookupResult) {
			// Dispatch a request to perform the computed userset lookup.
			computedUsersetRequest := cl.dispatch(LookupRequest{
				TargetONR: req.TargetONR,
				StartRelation: &v0.RelationReference{
					Namespace: directRelation.Namespace,
					Relation:  ttu.ComputedUserset.Relation,
				},
				Limit:          noLimit, // Since this is a step in the lookup.
				AtRevision:     req.AtRevision,
				DepthRemaining: req.DepthRemaining - 1,
				DirectStack:    req.DirectStack,
				TTUStack:       req.TTUStack.With(onr),
				DebugTracer:    computedUsersetTracer.Childf("%s#%s", directRelation.Namespace, ttu.ComputedUserset.Relation),
			})

			result := LookupAny(ctx, noLimit, []ReduceableLookupFunc{computedUsersetRequest})
			if result.Err != nil || len(result.ResolvedObjects) == 0 {
				resultChan <- result
				return
			}

			// For each computed userset object, collect the usersets and then perform a tupleset lookup.
			computedUsersetResultsTracer := computedUsersetTracer.Childf("Results")

			usersets := []*v0.ObjectAndRelation{}
			for _, resolvedObj := range result.ResolvedObjects {
				computedUsersetResultsTracer.Childf("tupleset from %s", tuple.StringONR(resolvedObj))

				// Determine the relation(s) to use or the tupleset. This is determined based on the allowed direct relations and
				// we always check for both the actual relation resolved, as well as `...`
				allowedRelations := []string{}
				allowedDirect, err := typeSystem.IsAllowedDirectRelation(ttu.Tupleset.Relation, resolvedObj.Namespace, resolvedObj.Relation)
				if err != nil {
					resultChan <- LookupResult{Err: err}
					return
				}

				if allowedDirect == namespace.DirectRelationValid {
					allowedRelations = append(allowedRelations, resolvedObj.Relation)
				}

				if resolvedObj.Relation != Ellipsis {
					allowedEllipsis, err := typeSystem.IsAllowedDirectRelation(ttu.Tupleset.Relation, resolvedObj.Namespace, Ellipsis)
					if err != nil {
						resultChan <- LookupResult{Err: err}
					}

					if allowedEllipsis == namespace.DirectRelationValid {
						allowedRelations = append(allowedRelations, Ellipsis)
					}
				}

				for _, allowedRelation := range allowedRelations {
					userset := &v0.ObjectAndRelation{
						Namespace: resolvedObj.Namespace,
						ObjectId:  resolvedObj.ObjectId,
						Relation:  allowedRelation,
					}
					usersets = append(usersets, userset)
				}
			}

			// Perform the tupleset lookup.
			objects := tuple.NewONRSet()
			if len(usersets) > 0 {
				it, err := cl.ds.QueryTuples(req.StartRelation.Namespace, req.AtRevision).
					WithRelation(ttu.Tupleset.Relation).
					WithUsersets(usersets).
					Limit(uint64(req.Limit)).
					Execute(ctx)
				if err != nil {
					resultChan <- LookupResult{Err: err}
					return
				}
				defer it.Close()

				for tpl := it.Next(); tpl != nil; tpl = it.Next() {
					if it.Err() != nil {
						resultChan <- LookupResult{Err: it.Err()}
						return
					}

					computedUsersetResultsTracer.Child(tuple.String(tpl))

					if tpl.ObjectAndRelation.Namespace != req.StartRelation.Namespace {
						resultChan <- LookupResult{Err: fmt.Errorf("got unexpected namespace")}
						return
					}

					objects.Add(&v0.ObjectAndRelation{
						Namespace: req.StartRelation.Namespace,
						ObjectId:  tpl.ObjectAndRelation.ObjectId,
						Relation:  req.StartRelation.Relation,
					})

					if objects.Length() >= req.Limit {
						break
					}
				}
			}

			ttuTracer.Add("ttu Results", EmittableObjectSet(*objects))
			resultChan <- LookupResult{ResolvedObjects: objects.AsSlice()}
		})
	}

	if len(requests) == 0 {
		return ResolvedObjects([]*v0.ObjectAndRelation{})
	}

	return func(ctx context.Context, resultChan chan<- LookupResult) {
		resultChan <- LookupAny(ctx, req.Limit, requests)
	}
}

func (cl *concurrentLookup) lookupComputed(ctx context.Context, req LookupRequest, tracer DebugTracer, cu *v0.ComputedUserset) ReduceableLookupFunc {
	result := LookupOne(ctx, cl.dispatch(LookupRequest{
		TargetONR: req.TargetONR,
		StartRelation: &v0.RelationReference{
			Namespace: req.StartRelation.Namespace,
			Relation:  cu.Relation,
		},
		Limit:          req.Limit,
		AtRevision:     req.AtRevision,
		DepthRemaining: req.DepthRemaining - 1,
		DirectStack: req.DirectStack.With(&v0.ObjectAndRelation{
			Namespace: req.StartRelation.Namespace,
			Relation:  req.StartRelation.Relation,
			ObjectId:  "",
		}),
		TTUStack:    req.TTUStack,
		DebugTracer: tracer.Childf("computed_userset %s", cu.Relation),
	}))

	if result.Err != nil {
		return ResolveError(result.Err)
	}

	// Rewrite the found ONRs to be this relation.
	rewrittenResolved := make([]*v0.ObjectAndRelation, 0, len(result.ResolvedObjects))
	for _, resolved := range result.ResolvedObjects {
		if resolved.Namespace != req.StartRelation.Namespace {
			return ResolveError(fmt.Errorf("invalid namespace: %s vs %s", tuple.StringONR(resolved), req.StartRelation.Namespace))
		}

		rewrittenResolved = append(rewrittenResolved,
			&v0.ObjectAndRelation{
				Namespace: resolved.Namespace,
				Relation:  req.StartRelation.Relation,
				ObjectId:  resolved.ObjectId,
			})
	}

	return ResolvedObjects(rewrittenResolved)
}

func (cl *concurrentLookup) dispatch(req LookupRequest) ReduceableLookupFunc {
	return func(ctx context.Context, resultChan chan<- LookupResult) {
		log.Trace().Object("dispatch lookup", req).Send()
		result := cl.d.Lookup(ctx, req)
		resultChan <- result
	}
}

func LookupOne(ctx context.Context, request ReduceableLookupFunc) LookupResult {
	childCtx, cancelFn := context.WithCancel(ctx)
	defer cancelFn()

	resultChan := make(chan LookupResult)
	go request(childCtx, resultChan)

	select {
	case result := <-resultChan:
		return result
	case <-ctx.Done():
		return LookupResult{Err: NewRequestCanceledErr()}
	}
}

func LookupAny(ctx context.Context, limit int, requests []ReduceableLookupFunc) LookupResult {
	childCtx, cancelFn := context.WithCancel(ctx)
	defer cancelFn()

	resultChans := make([]chan LookupResult, 0, len(requests))
	for _, req := range requests {
		resultChan := make(chan LookupResult)
		resultChans = append(resultChans, resultChan)
		go req(childCtx, resultChan)
	}

	objects := tuple.NewONRSet()
	for _, resultChan := range resultChans {
		select {
		case result := <-resultChan:
			if result.Err != nil {
				return LookupResult{Err: result.Err}
			}

			objects.Update(result.ResolvedObjects)

			if objects.Length() >= int(limit) {
				return LookupResult{
					ResolvedObjects: limitedSlice(objects.AsSlice(), limit),
				}
			}
		case <-ctx.Done():
			return LookupResult{Err: NewRequestCanceledErr()}
		}
	}

	return LookupResult{
		ResolvedObjects: limitedSlice(objects.AsSlice(), limit),
	}
}

func LookupAll(ctx context.Context, limit int, requests []ReduceableLookupFunc) LookupResult {
	if len(requests) == 0 {
		return LookupResult{[]*v0.ObjectAndRelation{}, nil}
	}

	resultChan := make(chan LookupResult, len(requests))
	childCtx, cancelFn := context.WithCancel(ctx)
	defer cancelFn()

	for _, req := range requests {
		go req(childCtx, resultChan)
	}

	objSet := tuple.NewONRSet()

	for i := 0; i < len(requests); i++ {
		select {
		case result := <-resultChan:
			if result.Err != nil {
				return result
			}

			subSet := tuple.NewONRSet()
			subSet.Update(result.ResolvedObjects)

			if i == 0 {
				objSet = subSet
			} else {
				objSet = objSet.Intersect(subSet)
			}

			if objSet.Length() == 0 {
				return LookupResult{[]*v0.ObjectAndRelation{}, nil}
			}
		case <-ctx.Done():
			return LookupResult{Err: NewRequestCanceledErr()}
		}
	}

	return LookupResult{objSet.AsSlice(), nil}
}

func LookupExclude(ctx context.Context, limit int, requests []ReduceableLookupFunc) LookupResult {
	childCtx, cancelFn := context.WithCancel(ctx)
	defer cancelFn()

	baseChan := make(chan LookupResult, 1)
	othersChan := make(chan LookupResult, len(requests)-1)

	go requests[0](childCtx, baseChan)
	for _, req := range requests[1:] {
		go req(childCtx, othersChan)
	}

	objSet := tuple.NewONRSet()
	excSet := tuple.NewONRSet()

	for i := 0; i < len(requests); i++ {
		select {
		case base := <-baseChan:
			if base.Err != nil {
				return base
			}
			objSet.Update(base.ResolvedObjects)

		case sub := <-othersChan:
			if sub.Err != nil {
				return sub
			}

			excSet.Update(sub.ResolvedObjects)
		case <-ctx.Done():
			return LookupResult{Err: NewRequestCanceledErr()}
		}
	}

	return LookupResult{limitedSlice(objSet.Subtract(excSet).AsSlice(), limit), nil}
}

func ResolvedObjects(resolved []*v0.ObjectAndRelation) ReduceableLookupFunc {
	return func(ctx context.Context, resultChan chan<- LookupResult) {
		resultChan <- LookupResult{ResolvedObjects: resolved}
	}
}

func Resolved(resolved *v0.ObjectAndRelation) ReduceableLookupFunc {
	return func(ctx context.Context, resultChan chan<- LookupResult) {
		resultChan <- LookupResult{ResolvedObjects: []*v0.ObjectAndRelation{resolved}}
	}
}

func ResolveError(err error) ReduceableLookupFunc {
	if err == nil {
		panic("Given nil error to ResolveError")
	}

	return func(ctx context.Context, resultChan chan<- LookupResult) {
		resultChan <- LookupResult{Err: err}
	}
}

func limitedSlice(slice []*v0.ObjectAndRelation, limit int) []*v0.ObjectAndRelation {
	if len(slice) > int(limit) {
		return slice[0:limit]
	}

	return slice
}

type EmittableObjectSlice []*v0.ObjectAndRelation

func (s EmittableObjectSlice) EmitForTrace(tracer DebugTracer) {
	for _, value := range s {
		tracer.Child(tuple.StringONR(value))
	}
}

type EmittableObjectSet tuple.ONRSet

func (s EmittableObjectSet) EmitForTrace(tracer DebugTracer) {
	onrset := tuple.ONRSet(s)
	for _, value := range onrset.AsSlice() {
		tracer.Child(tuple.StringONR(value))
	}
}

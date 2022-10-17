package test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/authzed/spicedb/internal/datastore/common"
	"github.com/authzed/spicedb/internal/testfixtures"
	"github.com/authzed/spicedb/pkg/caveats"
	"github.com/authzed/spicedb/pkg/datastore"
	core "github.com/authzed/spicedb/pkg/proto/core/v1"
	"github.com/authzed/spicedb/pkg/tuple"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/structpb"
)

func WriteReadDeleteCaveatTest(t *testing.T, tester DatastoreTester) {
	req := require.New(t)
	ds, err := tester.New(0*time.Second, veryLargeGCWindow, 1)
	req.NoError(err)

	skipIfNotCaveatStorer(t, ds)

	// Dupes in same transaction are fail to be written
	ctx := context.Background()
	coreCaveat := createCoreCaveat(t)
	_, err = writeCaveats(ctx, ds, coreCaveat, coreCaveat)
	req.Error(err)

	// Succeeds writing a caveat
	rev, err := writeCaveat(ctx, ds, coreCaveat)
	req.NoError(err)

	// The caveat can be looked up by name
	cr := ds.SnapshotReader(rev)
	cv, readRev, err := cr.ReadCaveatByName(ctx, coreCaveat.Name)
	req.NoError(err)

	foundDiff := cmp.Diff(coreCaveat, cv, protocmp.Transform())
	req.Empty(foundDiff)
	req.Equal(rev, readRev)

	// All caveats can be listed
	cvs, err := cr.ListCaveats(ctx)
	req.NoError(err)
	req.Len(cvs, 1)

	foundDiff = cmp.Diff(coreCaveat, cvs[0], protocmp.Transform())
	req.Empty(foundDiff)

	// Delete Caveat
	rev, err = ds.ReadWriteTx(ctx, func(ctx context.Context, tx datastore.ReadWriteTransaction) error {
		return tx.DeleteCaveats([]string{coreCaveat.Name})
	})
	req.NoError(err)
	cr = ds.SnapshotReader(rev)
	_, _, err = cr.ReadCaveatByName(ctx, coreCaveat.Name)
	req.ErrorAs(err, &datastore.ErrCaveatNameNotFound{})

	// Returns an error if caveat name or ID does not exist
	_, _, err = cr.ReadCaveatByName(ctx, "doesnotexist")
	req.ErrorAs(err, &datastore.ErrCaveatNameNotFound{})
}

func WriteCaveatedRelationshipTest(t *testing.T, tester DatastoreTester) {
	req := require.New(t)
	ds, err := tester.New(0*time.Second, veryLargeGCWindow, 1)
	req.NoError(err)

	skipIfNotCaveatStorer(t, ds)

	req.NoError(err)
	sds, _ := testfixtures.StandardDatastoreWithSchema(ds, req)

	// Store caveat, write caveated tuple and read back same value
	coreCaveat := createCoreCaveat(t)
	ctx := context.Background()
	_, err = writeCaveat(ctx, ds, coreCaveat)
	req.NoError(err)

	tpl := createTestCaveatedTuple(t, "document:companyplan#parent@folder:company#...", coreCaveat.Name)
	rev, err := common.WriteTuples(ctx, sds, core.RelationTupleUpdate_CREATE, tpl)
	req.NoError(err)
	iter, err := ds.SnapshotReader(rev).QueryRelationships(ctx, datastore.RelationshipsFilter{
		ResourceType: tpl.ResourceAndRelation.Namespace,
	})
	req.NoError(err)

	defer iter.Close()
	readTpl := iter.Next()
	foundDiff := cmp.Diff(tpl, readTpl, protocmp.Transform())
	req.Empty(foundDiff)

	// Caveated tuple can reference non-existing caveat - controller layer is responsible for validation
	tpl = createTestCaveatedTuple(t, "document:rando#parent@folder:company#...", "rando")
	_, err = common.WriteTuples(ctx, sds, core.RelationTupleUpdate_CREATE, tpl)
	req.NoError(err)
}

func CaveatedRelationshipFilterTest(t *testing.T, tester DatastoreTester) {
	req := require.New(t)
	ds, err := tester.New(0*time.Second, veryLargeGCWindow, 1)
	req.NoError(err)

	skipIfNotCaveatStorer(t, ds)

	req.NoError(err)
	sds, _ := testfixtures.StandardDatastoreWithSchema(ds, req)

	// Store caveat, write caveated tuple and read back same value
	coreCaveat := createCoreCaveat(t)
	anotherCoreCaveat := createCoreCaveat(t)
	ctx := context.Background()
	_, err = writeCaveats(ctx, ds, coreCaveat, anotherCoreCaveat)
	req.NoError(err)

	tpl := createTestCaveatedTuple(t, "document:companyplan#parent@folder:company#...", coreCaveat.Name)
	anotherTpl := createTestCaveatedTuple(t, "document:anothercompanyplan#parent@folder:company#...", anotherCoreCaveat.Name)
	nonCaveatedTpl := tuple.MustParse("document:yetanothercompanyplan#parent@folder:company#...")
	rev, err := common.WriteTuples(ctx, sds, core.RelationTupleUpdate_CREATE, tpl, anotherTpl, nonCaveatedTpl)
	req.NoError(err)

	// filter by first caveat
	iter, err := ds.SnapshotReader(rev).QueryRelationships(ctx, datastore.RelationshipsFilter{
		ResourceType:       tpl.ResourceAndRelation.Namespace,
		OptionalCaveatName: coreCaveat.Name,
	})
	req.NoError(err)

	expectTuple(req, iter, tpl)

	// filter by second caveat
	iter, err = ds.SnapshotReader(rev).QueryRelationships(ctx, datastore.RelationshipsFilter{
		ResourceType:       anotherTpl.ResourceAndRelation.Namespace,
		OptionalCaveatName: anotherCoreCaveat.Name,
	})
	req.NoError(err)

	expectTuple(req, iter, anotherTpl)
}

func expectTuple(req *require.Assertions, iter datastore.RelationshipIterator, tpl *core.RelationTuple) {
	defer iter.Close()
	readTpl := iter.Next()
	foundDiff := cmp.Diff(tpl, readTpl, protocmp.Transform())
	req.Empty(foundDiff)
	req.Nil(iter.Next())
}

func CaveatSnapshotReadsTest(t *testing.T, tester DatastoreTester) {
	req := require.New(t)
	ds, err := tester.New(0*time.Second, veryLargeGCWindow, 1)
	req.NoError(err)

	skipIfNotCaveatStorer(t, ds)

	// Write an initial caveat
	coreCaveat := createCoreCaveat(t)
	ctx := context.Background()
	oldRev, err := writeCaveat(ctx, ds, coreCaveat)
	req.NoError(err)

	// Modify caveat and update
	oldExpression := coreCaveat.SerializedExpression
	newExpression := []byte{0x0a}
	coreCaveat.SerializedExpression = newExpression
	newRev, err := writeCaveat(ctx, ds, coreCaveat)
	req.NoError(err)

	// check most recent revision
	cr := ds.SnapshotReader(newRev)
	cv, fetchedRev, err := cr.ReadCaveatByName(ctx, coreCaveat.Name)
	req.NoError(err)
	req.Equal(newExpression, cv.SerializedExpression)
	req.Equal(newRev, fetchedRev)

	// check previous revision
	cr = ds.SnapshotReader(oldRev)
	cv, fetchedRev, err = cr.ReadCaveatByName(ctx, coreCaveat.Name)
	req.NoError(err)
	req.Equal(oldExpression, cv.SerializedExpression)
	req.Equal(oldRev, fetchedRev)
}

func CaveatedRelationshipWatchTest(t *testing.T, tester DatastoreTester) {
	req := require.New(t)
	ds, err := tester.New(0*time.Second, veryLargeGCWindow, 16)
	req.NoError(err)

	skipIfNotCaveatStorer(t, ds)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Write caveat and caveated relationship
	coreCaveat := createCoreCaveat(t)
	_, err = writeCaveat(ctx, ds, coreCaveat)
	req.NoError(err)

	// TODO bug: Watch API won't send updates i revision used is the first revision
	lowestRevision, err := ds.HeadRevision(ctx)
	req.NoError(err)
	chanRevisionChanges, chanErr := ds.Watch(ctx, lowestRevision)
	req.Zero(len(chanErr))

	tpl := createTestCaveatedTuple(t, "document:companyplan#parent@folder:company#...", coreCaveat.Name)
	_, err = common.WriteTuples(ctx, ds, core.RelationTupleUpdate_CREATE, tpl)
	req.NoError(err)

	// Caveated Relationship should come through Watch API

	changeWait := time.NewTimer(5 * time.Second)
	select {
	case change, ok := <-chanRevisionChanges:
		req.True(ok)
		req.Len(change.Changes, 1)
		for _, update := range change.Changes {
			foundDiff := cmp.Diff(tpl, update.Tuple, protocmp.Transform())
			req.Empty(foundDiff)
		}
	case <-changeWait.C:
		req.Fail("timed out waiting for caveated relationship via Watch API")
	}
}

func skipIfNotCaveatStorer(t *testing.T, ds datastore.Datastore) {
	ctx := context.Background()
	_, _ = ds.ReadWriteTx(ctx, func(ctx context.Context, transaction datastore.ReadWriteTransaction) error { //nolint: errcheck
		_, _, err := transaction.ReadCaveatByName(ctx, uuid.NewString())
		if !errors.As(err, &datastore.ErrCaveatNameNotFound{}) {
			t.Skip("datastore does not implement CaveatStorer interface")
		}
		return fmt.Errorf("force rollback of unnecesary tx")
	})
}

func createTestCaveatedTuple(t *testing.T, tplString string, caveatName string) *core.RelationTuple {
	tpl := tuple.MustParse(tplString)
	st, err := structpb.NewStruct(map[string]interface{}{"a": 1, "b": "test"})
	require.NoError(t, err)

	tpl.Caveat = &core.ContextualizedCaveat{
		CaveatName: caveatName,
		Context:    st,
	}
	return tpl
}

func writeCaveats(ctx context.Context, ds datastore.Datastore, coreCaveat ...*core.CaveatDefinition) (datastore.Revision, error) {
	rev, err := ds.ReadWriteTx(ctx, func(ctx context.Context, tx datastore.ReadWriteTransaction) error {
		return tx.WriteCaveats(coreCaveat)
	})
	if err != nil {
		return datastore.NoRevision, err
	}
	return rev, err
}

func writeCaveat(ctx context.Context, ds datastore.Datastore, coreCaveat *core.CaveatDefinition) (datastore.Revision, error) {
	rev, err := writeCaveats(ctx, ds, coreCaveat)
	if err != nil {
		return datastore.NoRevision, err
	}
	return rev, nil
}

func createCoreCaveat(t *testing.T) *core.CaveatDefinition {
	t.Helper()
	c := createCompiledCaveat(t)
	cBytes, err := c.Serialize()
	require.NoError(t, err)

	coreCaveat := &core.CaveatDefinition{
		Name:                 c.Name(),
		SerializedExpression: cBytes,
	}
	require.NoError(t, err)

	return coreCaveat
}

func createCompiledCaveat(t *testing.T) *caveats.CompiledCaveat {
	t.Helper()
	env, err := caveats.EnvForVariables(map[string]caveats.VariableType{
		"a": caveats.IntType,
		"b": caveats.IntType,
	})
	require.NoError(t, err)

	c, err := caveats.CompileCaveatWithName(env, "a == b", uuid.New().String())
	require.NoError(t, err)

	return c
}
// Code generated by protoc-gen-go-grpc. DO NOT EDIT.

package apiv1alpha1

import (
	context "context"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.32.0 or later.
const _ = grpc.SupportPackageIsVersion7

// SchemaServiceClient is the client API for SchemaService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type SchemaServiceClient interface {
	// Read returns the current Object Definitions for a Permissions System.
	//
	// Errors include:
	// - INVALID_ARGUMENT: a provided value has failed to semantically validate
	// - NOT_FOUND: one of the Object Definitions being requested does not exist
	ReadSchema(ctx context.Context, in *ReadSchemaRequest, opts ...grpc.CallOption) (*ReadSchemaResponse, error)
	// Write overwrites the current Object Definitions for a Permissions System.
	//
	// Any Object Definitions that exist, but are not included will be deleted.
	WriteSchema(ctx context.Context, in *WriteSchemaRequest, opts ...grpc.CallOption) (*WriteSchemaResponse, error)
}

type schemaServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewSchemaServiceClient(cc grpc.ClientConnInterface) SchemaServiceClient {
	return &schemaServiceClient{cc}
}

func (c *schemaServiceClient) ReadSchema(ctx context.Context, in *ReadSchemaRequest, opts ...grpc.CallOption) (*ReadSchemaResponse, error) {
	out := new(ReadSchemaResponse)
	err := c.cc.Invoke(ctx, "/authzed.api.v1alpha1.SchemaService/ReadSchema", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *schemaServiceClient) WriteSchema(ctx context.Context, in *WriteSchemaRequest, opts ...grpc.CallOption) (*WriteSchemaResponse, error) {
	out := new(WriteSchemaResponse)
	err := c.cc.Invoke(ctx, "/authzed.api.v1alpha1.SchemaService/WriteSchema", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SchemaServiceServer is the server API for SchemaService service.
// All implementations must embed UnimplementedSchemaServiceServer
// for forward compatibility
type SchemaServiceServer interface {
	// Read returns the current Object Definitions for a Permissions System.
	//
	// Errors include:
	// - INVALID_ARGUMENT: a provided value has failed to semantically validate
	// - NOT_FOUND: one of the Object Definitions being requested does not exist
	ReadSchema(context.Context, *ReadSchemaRequest) (*ReadSchemaResponse, error)
	// Write overwrites the current Object Definitions for a Permissions System.
	//
	// Any Object Definitions that exist, but are not included will be deleted.
	WriteSchema(context.Context, *WriteSchemaRequest) (*WriteSchemaResponse, error)
	mustEmbedUnimplementedSchemaServiceServer()
}

// UnimplementedSchemaServiceServer must be embedded to have forward compatible implementations.
type UnimplementedSchemaServiceServer struct {
}

func (UnimplementedSchemaServiceServer) ReadSchema(context.Context, *ReadSchemaRequest) (*ReadSchemaResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ReadSchema not implemented")
}
func (UnimplementedSchemaServiceServer) WriteSchema(context.Context, *WriteSchemaRequest) (*WriteSchemaResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method WriteSchema not implemented")
}
func (UnimplementedSchemaServiceServer) mustEmbedUnimplementedSchemaServiceServer() {}

// UnsafeSchemaServiceServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to SchemaServiceServer will
// result in compilation errors.
type UnsafeSchemaServiceServer interface {
	mustEmbedUnimplementedSchemaServiceServer()
}

func RegisterSchemaServiceServer(s grpc.ServiceRegistrar, srv SchemaServiceServer) {
	s.RegisterService(&SchemaService_ServiceDesc, srv)
}

func _SchemaService_ReadSchema_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ReadSchemaRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SchemaServiceServer).ReadSchema(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/authzed.api.v1alpha1.SchemaService/ReadSchema",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SchemaServiceServer).ReadSchema(ctx, req.(*ReadSchemaRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SchemaService_WriteSchema_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(WriteSchemaRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SchemaServiceServer).WriteSchema(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/authzed.api.v1alpha1.SchemaService/WriteSchema",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SchemaServiceServer).WriteSchema(ctx, req.(*WriteSchemaRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// SchemaService_ServiceDesc is the grpc.ServiceDesc for SchemaService service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var SchemaService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "authzed.api.v1alpha1.SchemaService",
	HandlerType: (*SchemaServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "ReadSchema",
			Handler:    _SchemaService_ReadSchema_Handler,
		},
		{
			MethodName: "WriteSchema",
			Handler:    _SchemaService_WriteSchema_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "authzed/api/v1alpha1/schema.proto",
}

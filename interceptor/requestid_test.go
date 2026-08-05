package interceptor_test

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/yshengliao/gortexa/interceptor"
)

func TestRequestID(t *testing.T) {
	interceptorFn := interceptor.RequestID()

	// Test without incoming Request ID
	var handlerID string
	handler := func(ctx context.Context, req any) (any, error) {
		id, ok := interceptor.RequestIDFrom(ctx)
		if !ok {
			t.Error("RequestIDFrom returned false")
		}
		if id == "" {
			t.Error("RequestIDFrom returned empty ID")
		}
		handlerID = id
		return nil, nil
	}

	_, err := interceptorFn(context.Background(), nil, &grpc.UnaryServerInfo{}, handler)
	if err != nil {
		t.Fatal(err)
	}
	if handlerID == "" {
		t.Error("Handler did not receive Request ID")
	}

	// Test with incoming valid Request ID
	ctxWithMD := metadata.NewIncomingContext(context.Background(), metadata.Pairs(interceptor.RequestIDMetadataKey, "valid-id-123"))
	var handlerID2 string
	handler2 := func(ctx context.Context, req any) (any, error) {
		id, ok := interceptor.RequestIDFrom(ctx)
		if !ok {
			t.Error("RequestIDFrom returned false")
		}
		handlerID2 = id
		return nil, nil
	}

	_, err = interceptorFn(ctxWithMD, nil, &grpc.UnaryServerInfo{}, handler2)
	if err != nil {
		t.Fatal(err)
	}
	if handlerID2 != "valid-id-123" {
		t.Errorf("Handler received ID %q, want 'valid-id-123'", handlerID2)
	}

	// Test with incoming invalid Request ID
	ctxWithInvalidMD := metadata.NewIncomingContext(context.Background(), metadata.Pairs(interceptor.RequestIDMetadataKey, "invalid id!"))
	var handlerID3 string
	handler3 := func(ctx context.Context, req any) (any, error) {
		id, ok := interceptor.RequestIDFrom(ctx)
		if !ok {
			t.Error("RequestIDFrom returned false")
		}
		handlerID3 = id
		return nil, nil
	}

	_, err = interceptorFn(ctxWithInvalidMD, nil, &grpc.UnaryServerInfo{}, handler3)
	if err != nil {
		t.Fatal(err)
	}
	if handlerID3 == "" || handlerID3 == "invalid id!" {
		t.Errorf("Handler received ID %q, expected a newly generated valid ID", handlerID3)
	}
}

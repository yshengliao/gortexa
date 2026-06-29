package testutil_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	"github.com/yshengliao/gortexa/internal/auth"
	"github.com/yshengliao/gortexa/internal/logic"
	"github.com/yshengliao/gortexa/testutil"
)

func authCtx(t *testing.T) context.Context {
	t.Helper()
	v := auth.NewVerifier(testutil.DefaultSecret, "gortexa")
	tok, err := v.Sign("tester", []string{"admin"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+tok))
}

func TestFullChainEndToEnd(t *testing.T) {
	conn := testutil.NewTestServer(t, func(s *grpc.Server) {
		resourcev1.RegisterResourceServiceServer(s, logic.NewResourceService())
	})
	client := resourcev1.NewResourceServiceClient(conn)
	ctx := context.Background()

	// 1. unauthenticated → the auth interceptor rejects before the handler.
	if _, err := client.GetResource(ctx, &resourcev1.GetResourceRequest{Id: "x"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated GetResource code = %v, want Unauthenticated", status.Code(err))
	}

	actx := authCtx(t)

	// 2. validation rejects an empty id with InvalidArgument.
	if _, err := client.GetResource(actx, &resourcev1.GetResourceRequest{Id: ""}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty-id code = %v, want InvalidArgument", status.Code(err))
	}

	// 3. create → get round-trip through the full chain.
	created, err := client.CreateResource(actx, &resourcev1.CreateResourceRequest{
		Resource: &resourcev1.Resource{Name: "alpha", Owner: "u-1"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.GetId() == "" || created.GetStatus() != resourcev1.Status_STATUS_ACTIVE {
		t.Fatalf("created = %+v", created)
	}
	got, err := client.GetResource(actx, &resourcev1.GetResourceRequest{Id: created.GetId()})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.GetName() != "alpha" {
		t.Fatalf("got name = %q", got.GetName())
	}

	// 4. missing resource → NotFound (handler error mapped through *Error).
	if _, err := client.GetResource(actx, &resourcev1.GetResourceRequest{Id: "does-not-exist"}); status.Code(err) != codes.NotFound {
		t.Fatalf("missing code = %v, want NotFound", status.Code(err))
	}
}

func TestMain(m *testing.M) { testutil.VerifyTestMain(m) }

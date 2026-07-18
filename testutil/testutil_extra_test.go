package testutil_test

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/yshengliao/gortexa/auth"
	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	"github.com/yshengliao/gortexa/interceptor"
	"github.com/yshengliao/gortexa/internal/logic"
	"github.com/yshengliao/gortexa/testutil"
)

func TestNewTestServerWithInterceptorSet(t *testing.T) {
	// Build the default chain, then replace auth with a pass-through so an
	// unauthenticated call reaches the handler — proving the override is used.
	set, err := interceptor.NewSet(interceptor.Config{
		Verifier: auth.MustNewVerifier(testutil.DefaultSecret, "gortexa"),
	})
	if err != nil {
		t.Fatal(err)
	}
	set.Auth = func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(ctx, req)
	}
	set.AuthStream = func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, ss)
	}

	conn := testutil.NewTestServer(t, func(s *grpc.Server) {
		resourcev1.RegisterResourceServiceServer(s, logic.NewResourceService())
	}, testutil.WithInterceptorSet(set))
	client := resourcev1.NewResourceServiceClient(conn)

	// Without credentials: NotFound (handler reached), not Unauthenticated.
	if _, err := client.GetResource(context.Background(), &resourcev1.GetResourceRequest{Id: "missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound (auth override not applied)", status.Code(err))
	}
}

func TestNewTestServerWithAuthSecret(t *testing.T) {
	secret := []byte("fedcba9876543210fedcba9876543210")
	conn := testutil.NewTestServer(t, func(s *grpc.Server) {
		resourcev1.RegisterResourceServiceServer(s, logic.NewResourceService())
	}, testutil.WithAuthSecret(secret))
	client := resourcev1.NewResourceServiceClient(conn)

	// A token signed with the default secret must be rejected.
	if _, err := client.GetResource(authCtx(t), &resourcev1.GetResourceRequest{Id: "x"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("default-secret token code = %v, want Unauthenticated", status.Code(err))
	}
}

func TestGoldenUpdateAndMatch(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := flag.Set("update", "true"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := flag.Set("update", "false"); err != nil {
			t.Fatal(err)
		}
	}()

	data := []byte("hello golden\n")
	testutil.Golden(t, "sample", data)

	written, err := os.ReadFile(filepath.Join("testdata", "sample.golden"))
	if err != nil {
		t.Fatalf("golden file not written: %v", err)
	}
	if string(written) != string(data) {
		t.Fatalf("golden content = %q, want %q", written, data)
	}

	// Compare path: identical content must pass.
	if err := flag.Set("update", "false"); err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "sample", data)
}

func TestGoleakOptions(t *testing.T) {
	opts := testutil.GoleakOptions()
	if len(opts) != 4 {
		t.Fatalf("len(GoleakOptions()) = %d, want 4", len(opts))
	}
	for i, o := range opts {
		if o == nil {
			t.Errorf("option %d is nil", i)
		}
	}
}

func TestAssertNoLeakClean(t *testing.T) {
	testutil.AssertNoLeak(t)
}

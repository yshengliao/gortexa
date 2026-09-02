package main

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/yshengliao/gortexa/auth"
	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	"github.com/yshengliao/gortexa/interceptor"
	"github.com/yshengliao/gortexa/internal/logic"
	"github.com/yshengliao/gortexa/testutil"
)

// TestAuthSkip pins the auth-exemption surface: health is always exempt,
// reflection only under the flag, and nothing else — a widened prefix (e.g.
// "/grpc.") would silently disable auth for every gRPC-namespaced service.
func TestAuthSkip(t *testing.T) {
	cases := []struct {
		name       string
		reflection bool
		method     string
		want       bool
	}{
		{"health exempt without reflection", false, "/grpc.health.v1.Health/Check", true},
		{"health exempt with reflection", true, "/grpc.health.v1.Health/Check", true},
		{"reflection blocked when disabled", false, "/grpc.reflection.v1.ServerReflection/ServerReflectionInfo", false},
		{"reflection exempt when enabled", true, "/grpc.reflection.v1.ServerReflection/ServerReflectionInfo", true},
		{"v1alpha reflection exempt when enabled", true, "/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo", true},
		{"prefix cannot leak past the dot", true, "/grpc.reflectionx.Evil/Method", false},
		{"health prefix cannot leak past the dot", true, "/grpc.healthx.Evil/Method", false},
		{"domain services stay authenticated", true, "/resource.v1.ResourceService/ListResources", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := authSkip(tc.reflection)(tc.method); got != tc.want {
				t.Errorf("authSkip(%v)(%q) = %v, want %v", tc.reflection, tc.method, got, tc.want)
			}
		})
	}
}

// TestLoadSheddingConfig_ReflectionExemptWhenAuthExempt reproduces R2-H1-1:
// run() wires AuthSkip to exempt "/grpc.reflection." only when reflection is
// enabled, and loadSheddingConfig must exempt the exact same surface from the
// inflight budget — otherwise an unauthenticated client can open idle
// ServerReflectionInfo streams (auth-exempt, but never released because a
// stream holds its slot for its whole lifetime) until MaxInflight is pinned
// and every other tenant's RPCs are shed with ResourceExhausted.
func TestLoadSheddingConfig_ReflectionExemptWhenAuthExempt(t *testing.T) {
	const maxInflight = 4

	lsCfg := loadSheddingConfig(true)
	lsCfg.MaxInflight = maxInflight

	// started fires once per idle reflection stream, only after that stream
	// has actually been admitted past the load-shedding interceptor — so the
	// test can wait for all maxInflight slots to be consumed deterministically
	// instead of racing the async stream handshake.
	started := make(chan struct{}, maxInflight)

	// reflectionDesc registers a fake ServerReflectionInfo stream whose
	// handler blocks until canceled — an idle caller that opens the stream
	// and never writes to it, exactly like the attacker in the claim.
	reflectionDesc := grpc.ServiceDesc{
		ServiceName: "grpc.reflection.v1.ServerReflection",
		HandlerType: (*any)(nil),
		Streams: []grpc.StreamDesc{
			{
				StreamName:    "ServerReflectionInfo",
				ServerStreams: true,
				ClientStreams: true,
				Handler: func(_ any, stream grpc.ServerStream) error {
					started <- struct{}{}
					<-stream.Context().Done()
					return stream.Context().Err()
				},
			},
		},
		Metadata: "grpc/reflection/v1/reflection.proto",
	}

	set, err := interceptor.NewSet(interceptor.Config{
		Verifier:     auth.MustNewVerifier(testutil.DefaultSecret, "gortexa"),
		AuthSkip:     authSkip(true),
		LoadShedding: lsCfg,
	})
	if err != nil {
		t.Fatalf("build interceptor set: %v", err)
	}

	conn := testutil.NewTestServer(t, func(s *grpc.Server) {
		resourcev1.RegisterResourceServiceServer(s, logic.NewResourceService())
		s.RegisterService(&reflectionDesc, nil)
	}, testutil.WithInterceptorSet(set))

	// Open maxInflight idle, unauthenticated ServerReflectionInfo streams and
	// wait until each has actually been admitted past load shedding.
	var cancels []context.CancelFunc
	t.Cleanup(func() {
		for _, cancel := range cancels {
			cancel()
		}
	})
	for i := 0; i < maxInflight; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		if _, err := conn.NewStream(ctx, &grpc.StreamDesc{
			StreamName:    "ServerReflectionInfo",
			ServerStreams: true,
			ClientStreams: true,
		}, "/grpc.reflection.v1.ServerReflection/ServerReflectionInfo"); err != nil {
			t.Fatalf("open idle reflection stream %d: %v", i, err)
		}
	}
	for i := 0; i < maxInflight; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("reflection stream %d never reached its handler (never admitted)", i)
		}
	}

	// A legitimate authenticated tenant call must not be shed by the idle,
	// auth-exempt reflection streams above.
	v := auth.MustNewVerifier(testutil.DefaultSecret, "gortexa")
	tok, err := v.Sign("tester", nil, time.Hour)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	actx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+tok))

	client := resourcev1.NewResourceServiceClient(conn)
	_, err = client.GetResource(actx, &resourcev1.GetResourceRequest{Id: "does-not-exist"})
	if status.Code(err) == codes.ResourceExhausted {
		t.Fatalf("authenticated GetResource was load-shed by idle, auth-exempt reflection streams: %v "+
			"(reflection is exempt from auth but not from the inflight budget)", err)
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("authenticated GetResource = %v, want NotFound (not ResourceExhausted)", err)
	}
}

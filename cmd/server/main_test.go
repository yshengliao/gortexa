package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/yshengliao/gortexa/auth"
	"github.com/yshengliao/gortexa/config"
	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	"github.com/yshengliao/gortexa/interceptor"
	"github.com/yshengliao/gortexa/internal/logic"
	"github.com/yshengliao/gortexa/mcp"
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
		{"health-like package stays authenticated", true, "/grpc.healthx.v1.Health/Check", false},
		{"user health service stays authenticated", true, "/app.health.v1.Health/Check", false},
		{"health service-name prefix stays authenticated", true, "/grpc.health.v1.HealthAdmin/Reset", false},
		{"reflection service-name prefix stays authenticated", true, "/grpc.reflection.v1.ServerReflectionAdmin/Reset", false},
		{"empty method stays authenticated", true, "", false},
		{"domain services stay authenticated", true, "/resource.v1.ResourceService/ListResources", false},
		{"user service in grpc.health package stays authenticated", true, "/grpc.health.v1.RecordService/CreateRecord", false},
		{"user service in grpc.reflection package stays authenticated", true, "/grpc.reflection.v1.RecordService/DeleteRecord", false},
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

// TestNewVerifierPicksScheme pins the auth wiring: jwks_url selects the
// key-set verifier (which cannot sign), otherwise the HS256 secret is used.
func TestNewVerifierPicksScheme(t *testing.T) {
	ctx := context.Background()
	hs, err := newVerifier(ctx, config.AuthConfig{JWTSecret: "0123456789abcdef0123456789abcdef", Issuer: "gortexa"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hs.Sign("u", nil, time.Hour); err != nil {
		t.Fatalf("HS256 verifier should sign: %v", err)
	}
	if _, err := newVerifier(ctx, config.AuthConfig{JWKSURL: "http://issuer.example/jwks", Issuer: "gortexa"}); err == nil {
		t.Fatal("jwks_url over plain http to a remote host should fail startup")
	}
}

// TestLoadSheddingSkipMatchesAuthSkip pins that the inflight-budget exemption
// is exactly the auth exemption for both reflection settings: a method exempt
// from auth but counted against the budget lets an unauthenticated client pin
// it, and one exempt from the budget but not from auth widens nothing useful.
func TestLoadSheddingSkipMatchesAuthSkip(t *testing.T) {
	methods := []string{
		"/grpc.health.v1.Health/Check",
		"/grpc.health.v1.Health/Watch",
		"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo",
		"/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo",
		"/grpc.healthx.v1.Health/Check",
		"/app.health.v1.Health/Check",
		"/resource.v1.ResourceService/ListResources",
	}
	for _, reflection := range []bool{false, true} {
		ls := loadSheddingConfig(reflection)
		if ls.MaxInflight <= 0 {
			t.Fatalf("MaxInflight = %d, want a positive budget", ls.MaxInflight)
		}
		skip := authSkip(reflection)
		for _, m := range methods {
			if got, want := ls.Skip(m), skip(m); got != want {
				t.Errorf("reflection=%v: loadshed skip(%q) = %v, auth skip = %v", reflection, m, got, want)
			}
		}
	}
}

// TestMCPServicesMatchRegisteredServices pins that every gRPC service run()
// registers is also exposed over MCP (and vice versa), and that every listed
// name resolves in the compiled-in registry — run() fails startup otherwise.
// It reads the registrations from main.go so a `gortexa gen` insertion that
// misses one of its three markers fails here, not in production.
func TestMCPServicesMatchRegisteredServices(t *testing.T) {
	names := mcpServices()
	if _, err := mcp.ServiceDescriptors(names...); err != nil {
		t.Fatalf("mcpServices() does not resolve: %v", err)
	}
	listed := map[string]bool{}
	for _, n := range names {
		if listed[string(n.Name())] {
			t.Fatalf("service %q listed twice", n)
		}
		listed[string(n.Name())] = true
	}

	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	servers := map[string]bool{}
	handlers := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !strings.HasPrefix(sel.Sel.Name, "Register") {
			return true
		}
		name := strings.TrimPrefix(sel.Sel.Name, "Register")
		if svc, ok := strings.CutSuffix(name, "Server"); ok {
			servers[svc] = true
		} else if svc, ok := strings.CutSuffix(name, "Handler"); ok {
			handlers[svc] = true
		}
		return true
	})
	if len(servers) == 0 {
		t.Fatal("found no Register*Server calls in main.go")
	}
	for svc := range servers {
		if !listed[svc] {
			t.Errorf("%s is registered on gRPC but missing from mcpServices()", svc)
		}
		if !handlers[svc] {
			t.Errorf("%s is registered on gRPC but has no gateway handler", svc)
		}
	}
	for svc := range listed {
		if !servers[svc] {
			t.Errorf("%s is listed in mcpServices() but never registered on gRPC", svc)
		}
	}
}

// TestExportSchemas pins the -export-ai-schemas path: every supported format
// renders valid JSON naming the sample service's tools without any config or
// listener, and an unknown format fails instead of printing nothing.
func TestExportSchemas(t *testing.T) {
	for _, format := range []string{"mcp", "openai", "gemini"} {
		t.Run(format, func(t *testing.T) {
			out := captureStdout(t, func() error { return exportSchemas(format) })
			if !json.Valid(out) {
				t.Fatalf("output is not JSON: %.200s", out)
			}
			if !bytes.Contains(out, []byte("Resource")) {
				t.Fatalf("output names no resource tool: %.200s", out)
			}
		})
	}
	t.Run("unknown format", func(t *testing.T) {
		var err error
		out := captureStdout(t, func() error { err = exportSchemas("yaml"); return nil })
		if err == nil || len(out) != 0 {
			t.Fatalf("exportSchemas(yaml) = %v with %d bytes, want an error and no output", err, len(out))
		}
	})
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func() error) []byte {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	ferr := fn()
	os.Stdout = old
	_ = w.Close()
	out := <-done
	_ = r.Close()
	if ferr != nil {
		t.Fatal(ferr)
	}
	return out
}

// TestConfigOptions pins config-file discovery: GORTEXA_CONFIG names the
// file, etc/config.yaml is the fallback, and a missing file is skipped (env
// and defaults still apply) rather than failing startup.
func TestConfigOptions(t *testing.T) {
	writeCfg := func(t *testing.T, path, addr string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("server:\n  addr: \""+addr+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	addr := func(t *testing.T) string {
		t.Helper()
		opts := append(configOptions(), config.WithEnviron(func() []string { return nil }))
		cfg, err := config.BuildUnvalidated(opts...)
		if err != nil {
			t.Fatal(err)
		}
		return cfg.Server.Addr
	}

	t.Run("GORTEXA_CONFIG wins", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		writeCfg(t, filepath.Join(dir, "etc", "config.yaml"), ":1111")
		explicit := filepath.Join(dir, "custom.yaml")
		writeCfg(t, explicit, ":2222")
		t.Setenv("GORTEXA_CONFIG", explicit)
		if got := addr(t); got != ":2222" {
			t.Fatalf("addr = %q, want :2222 from GORTEXA_CONFIG", got)
		}
	})
	t.Run("etc/config.yaml fallback", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		writeCfg(t, filepath.Join(dir, "etc", "config.yaml"), ":1111")
		t.Setenv("GORTEXA_CONFIG", "")
		if got := addr(t); got != ":1111" {
			t.Fatalf("addr = %q, want :1111 from etc/config.yaml", got)
		}
	})
	t.Run("missing file is skipped", func(t *testing.T) {
		t.Chdir(t.TempDir())
		t.Setenv("GORTEXA_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
		if n := len(configOptions()); n != 0 {
			t.Fatalf("configOptions() = %d options for a missing file, want 0", n)
		}
		if got := addr(t); got != ":8080" {
			t.Fatalf("addr = %q, want the :8080 default", got)
		}
	})
}

// TestNewVerifierJWKS pins the key-set branch end to end: a loopback JWKS
// endpoint is fetched at startup and yields a verify-only verifier, and an
// unreachable one fails startup instead of starting with no keys.
func TestNewVerifierJWKS(t *testing.T) {
	ctx := context.Background()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := k.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	body, err := json.Marshal(map[string]any{"keys": []map[string]string{
		{"kty": "EC", "kid": "k1", "crv": "P-256", "x": enc(b[1:33]), "y": enc(b[33:])},
	}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()

	// A JWT secret alongside jwks_url is ignored: the key-set verifier wins.
	v, err := newVerifier(ctx, config.AuthConfig{JWKSURL: srv.URL, JWTSecret: "0123456789abcdef0123456789abcdef", Issuer: "gortexa"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Sign("u", nil, time.Hour); err == nil {
		t.Fatal("jwks_url selected an HS256 verifier: it signed a token")
	}

	srv.Close()
	if _, err := newVerifier(ctx, config.AuthConfig{JWKSURL: srv.URL, Issuer: "gortexa"}); err == nil {
		t.Fatal("unreachable jwks_url should fail startup")
	}
	if _, err := newVerifier(ctx, config.AuthConfig{JWTSecret: "short", Issuer: "gortexa"}); err == nil {
		t.Fatal("a short HS256 secret should fail startup")
	}
}

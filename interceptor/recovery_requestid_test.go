package interceptor_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/yshengliao/gortexa/auth"
	"github.com/yshengliao/gortexa/interceptor"
)

// TestRecoveryLogsRequestID verifies the single highest-value correlation
// case: a client sends X-Request-Id and the handler panics. The resulting
// "panic recovered" log line — the only log record a panicking RPC produces,
// since Logger's own "rpc" record never runs when the panic unwinds past it —
// must carry request_id so the record can be joined back to the client's id.
//
// Recovery is the outermost stage in the fixed chain (recovery, requestid,
// logger, ...). logPanic reads the request id from Recovery's own ctx
// parameter, which is never reassigned after RequestID (an inner stage)
// derives its own request-id-bearing context and passes it further inward —
// that derived context never flows back out to Recovery's stack frame.
func TestRecoveryLogsRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	set, err := interceptor.NewSet(interceptor.Config{
		Verifier: auth.MustNewVerifier(jwtSecret, "gortexa"),
		Logger:   logger,
		AuthSkip: func(string) bool { return true },
	})
	if err != nil {
		t.Fatalf("build interceptor set: %v", err)
	}

	const clientID = "client-supplied-id-123"
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs(interceptor.RequestIDMetadataKey, clientID))

	chain := set.ChainUnary()
	_, _ = chain(ctx, nil, unaryInfo("/pkg.Svc/Boom"), func(context.Context, any) (any, error) {
		panic("boom")
	})

	logged := buf.String()
	if !strings.Contains(logged, "panic recovered") {
		t.Fatalf("expected a panic-recovered log line, got: %s", logged)
	}
	if !strings.Contains(logged, clientID) {
		t.Fatalf("panic log line is missing the client-supplied request_id %q; got: %s", clientID, logged)
	}
}

// TestRecoveryStreamLogsMintedRequestID is the streaming counterpart, with no
// inbound id: the id RequestIDStream mints must still reach the panic record,
// otherwise the id echoed to the client in the response header has nothing to
// join to on the server side.
func TestRecoveryStreamLogsMintedRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	recovery := interceptor.RecoveryStream(logger)
	requestID := interceptor.RequestIDStream()

	var minted string
	err := recovery(nil, &fakeStream{ctx: context.Background()}, streamInfo(),
		func(srv any, ss grpc.ServerStream) error {
			return requestID(srv, ss, streamInfo(), func(_ any, inner grpc.ServerStream) error {
				minted, _ = interceptor.RequestIDFrom(inner.Context())
				panic("stream boom")
			})
		})
	if err == nil {
		t.Fatal("expected the panic to be converted into an error")
	}
	if minted == "" {
		t.Fatal("RequestIDStream did not mint an id")
	}

	logged := buf.String()
	if !strings.Contains(logged, "panic recovered") {
		t.Fatalf("expected a panic-recovered log line, got: %s", logged)
	}
	if !strings.Contains(logged, minted) {
		t.Fatalf("stream panic log line is missing the minted request_id %q; got: %s", minted, logged)
	}
}

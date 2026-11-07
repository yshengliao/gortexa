package kernel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/yshengliao/gortexa/config"
)

// rawH2Client speaks just enough cleartext HTTP/2 (prior knowledge) to open a
// gRPC stream frame by frame. A stock grpc-go client always sends the request
// message, so only hand-rolled frames can leave a stream parked after HEADERS.
type rawH2Client struct {
	t    *testing.T
	conn net.Conn
	fr   *http2.Framer
}

func dialRawH2(t *testing.T, addr string) *rawH2Client {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
		t.Fatal(err)
	}
	fr := http2.NewFramer(conn, conn)
	if err := fr.WriteSettings(); err != nil {
		t.Fatal(err)
	}
	return &rawH2Client{t: t, conn: conn, fr: fr}
}

// openGRPCStream sends HEADERS for a gRPC POST without END_STREAM and without
// any DATA frame: the request message never arrives.
func (c *rawH2Client) openGRPCStream(streamID uint32, path string) {
	c.t.Helper()
	var hb bytes.Buffer
	enc := hpack.NewEncoder(&hb)
	for _, f := range []hpack.HeaderField{
		{Name: ":method", Value: "POST"},
		{Name: ":scheme", Value: "http"},
		{Name: ":path", Value: path},
		{Name: ":authority", Value: "localhost"},
		{Name: "content-type", Value: "application/grpc"},
		{Name: "te", Value: "trailers"},
	} {
		if err := enc.WriteField(f); err != nil {
			c.t.Fatal(err)
		}
	}
	if err := c.fr.WriteHeaders(http2.HeadersFrameParam{
		StreamID:      streamID,
		BlockFragment: hb.Bytes(),
		EndHeaders:    true,
	}); err != nil {
		c.t.Fatal(err)
	}
}

// waitForStreamEnd reads frames until the server ends streamID (RST_STREAM, or
// HEADERS with END_STREAM carrying a trailers-only status) or GOAWAY/close, or
// the deadline passes. It answers PINGs and ignores everything else.
func (c *rawH2Client) waitForStreamEnd(streamID uint32, within time.Duration) (ended bool, err error) {
	_ = c.conn.SetReadDeadline(time.Now().Add(within))
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()
	for {
		f, err := c.fr.ReadFrame()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return false, nil
			}
			// A closed connection ends every stream on it.
			return true, err
		}
		switch f := f.(type) {
		case *http2.RSTStreamFrame:
			if f.StreamID == streamID {
				return true, nil
			}
		case *http2.HeadersFrame:
			if f.StreamID == streamID && f.StreamEnded() {
				return true, nil
			}
		case *http2.GoAwayFrame:
			return true, nil
		case *http2.PingFrame:
			if !f.IsAck() {
				_ = c.fr.WritePing(true, f.Data)
			}
		case *http2.SettingsFrame:
			if !f.IsAck() {
				_ = c.fr.WriteSettingsAck()
			}
		}
	}
}

func startRawServer(t *testing.T, firstMsg time.Duration) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Server: config.ServerConfig{
		ShutdownTimeout:         time.Second,
		ReadHeaderTimeout:       time.Second,
		GRPCFirstMessageTimeout: firstMsg,
	}}
	app, err := New(WithConfig(cfg), WithLogger(quiet()), WithoutInterceptors())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- app.serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("serve returned error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("serve did not return after cancel")
		}
	})
	return ln.Addr().String()
}

// TestHeadersOnlyGRPCStreamIsReaped pins the first-message deadline on the
// external gRPC path. In grpc-go's ServeHTTP mode the generated handler reads
// the request message before any interceptor runs, so a stream that sends
// HEADERS and then nothing is invisible to load shedding, rate limiting and
// auth, and nothing else on the h2c server (ReadTimeout is deliberately 0,
// IdleTimeout only fires on a connection with no streams) ever ends it. Without
// the deadline such streams accumulate until the per-connection stream limit,
// one goroutine plus a handler transport each, for as long as the client keeps
// the connection open.
func TestHeadersOnlyGRPCStreamIsReaped(t *testing.T) {
	const firstMsg = 300 * time.Millisecond
	addr := startRawServer(t, firstMsg)
	c := dialRawH2(t, addr)
	c.openGRPCStream(1, "/grpc.health.v1.Health/Check")

	ended, err := c.waitForStreamEnd(1, 10*firstMsg)
	if err != nil && !ended {
		t.Fatal(err)
	}
	if !ended {
		t.Fatalf("stream with HEADERS but no request message was still open %v after the first-message deadline", 10*firstMsg)
	}
}

// TestGRPCStreamDeadlineClearsAfterFirstRead pins the other half: the deadline
// is a first-message deadline, not a body deadline. Once bytes of the request
// arrive the stream is in the handler's hands (and, for a streaming RPC, in the
// chain's), so a slow but live client must not be reset by it.
func TestGRPCStreamDeadlineClearsAfterFirstRead(t *testing.T) {
	const firstMsg = 300 * time.Millisecond
	addr := startRawServer(t, firstMsg)
	c := dialRawH2(t, addr)
	c.openGRPCStream(1, "/grpc.health.v1.Health/Check")
	// A partial gRPC frame: the 5-byte length-prefix header announcing a
	// 2-byte message that never follows. The transport reads these bytes (the
	// first Read succeeds) and then waits for the rest.
	if err := c.fr.WriteData(1, false, []byte{0, 0, 0, 0, 2}); err != nil {
		t.Fatal(err)
	}

	ended, _ := c.waitForStreamEnd(1, 4*firstMsg)
	if ended {
		t.Fatalf("stream was ended after its first request bytes arrived: the first-message deadline must be cleared on the first successful read")
	}
}

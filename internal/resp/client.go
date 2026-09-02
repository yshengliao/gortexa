package resp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// ErrNil is returned by Get when the key does not exist (a RESP null bulk).
var ErrNil = errors.New("resp: nil")

var errClosed = errors.New("resp: client is closed")

// Default tunables (used when the corresponding Option is zero).
const (
	defaultPoolSize     = 10
	defaultDialTimeout  = 5 * time.Second
	defaultReadTimeout  = 3 * time.Second
	defaultWriteTimeout = 3 * time.Second
)

// Options configures a Client. A zero timeout uses the default; a negative one
// disables that deadline (only the caller ctx bounds the operation).
type Options struct {
	Addr         string
	Password     string
	DB           int
	PoolSize     int
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

// Client is a bounded-pool RESP2 client. Connections dial lazily on first use.
type Client struct {
	opts Options
	// sem bounds the number of connections checked out at once (and therefore
	// the number ever dialed concurrently), capping total live connections at
	// PoolSize. A token is held for the duration of one command.
	sem chan struct{}

	mu     sync.Mutex
	idle   []*conn // parked connections, each holding no token
	closed bool
}

// NewClient builds a Client. It performs no I/O — the first command dials.
func NewClient(opts Options) *Client {
	if opts.PoolSize <= 0 {
		opts.PoolSize = defaultPoolSize
	}
	if opts.DialTimeout == 0 {
		opts.DialTimeout = defaultDialTimeout
	}
	if opts.ReadTimeout == 0 {
		opts.ReadTimeout = defaultReadTimeout
	}
	if opts.WriteTimeout == 0 {
		opts.WriteTimeout = defaultWriteTimeout
	}
	return &Client{opts: opts, sem: make(chan struct{}, opts.PoolSize)}
}

// Options returns the effective options (after defaults are applied), for
// introspection and tests.
func (c *Client) Options() Options { return c.opts }

type conn struct {
	nc net.Conn
	br *bufio.Reader
	bw *bufio.Writer
}

func (cn *conn) close() { _ = cn.nc.Close() }

// deadline returns the earlier of now+timeout and the caller ctx deadline; a
// zero time means "no deadline" (timeout disabled and ctx has none).
func deadline(ctx context.Context, timeout time.Duration) time.Time {
	var d time.Time
	if timeout > 0 {
		d = time.Now().Add(timeout)
	}
	if cd, ok := ctx.Deadline(); ok && (d.IsZero() || cd.Before(d)) {
		d = cd
	}
	return d
}

func (c *Client) dial(ctx context.Context) (*conn, error) {
	// A negative DialTimeout means "disabled" (see Options): leave the Dialer
	// timeout unset rather than handing it an already-expired deadline.
	d := net.Dialer{}
	if c.opts.DialTimeout > 0 {
		d.Timeout = c.opts.DialTimeout
	}
	nc, err := d.DialContext(ctx, "tcp", c.opts.Addr)
	if err != nil {
		return nil, err
	}
	cn := &conn{nc: nc, br: bufio.NewReader(nc), bw: bufio.NewWriter(nc)}
	if c.opts.Password != "" {
		if err := c.command(ctx, cn, "AUTH", c.opts.Password); err != nil {
			cn.close()
			return nil, err
		}
	}
	if c.opts.DB != 0 {
		if err := c.command(ctx, cn, "SELECT", c.opts.DB); err != nil {
			cn.close()
			return nil, err
		}
	}
	return cn, nil
}

// command writes args and reads one reply on cn, applying per-command deadlines.
func (c *Client) command(ctx context.Context, cn *conn, args ...any) error {
	_, err := c.roundtrip(ctx, cn, args...)
	return err
}

func (c *Client) roundtrip(ctx context.Context, cn *conn, args ...any) (any, error) {
	// deadline() only ever consults ctx.Deadline(), so a ctx carrying nothing
	// but cancellation would never reach the socket: the I/O would run to its
	// own timeout, or — with that timeout disabled (see Options) — block
	// forever while holding a pool token that Close cannot reclaim. Tripping
	// the connection deadline on cancel unblocks whichever direction is in
	// flight; the resulting error is not a resp.Error, so Do discards the
	// connection rather than parking one with a poisoned deadline.
	if ctx.Done() != nil {
		stop := context.AfterFunc(ctx, func() { _ = cn.nc.SetDeadline(time.Now()) })
		defer stop()
	}
	// Set deadlines unconditionally — a zero time clears any deadline. Setting
	// them only when non-zero would let a deadline armed by a previous command
	// (e.g. from that command's ctx) persist on a pooled connection and fire
	// spuriously on the next reuse when this command has no deadline of its own.
	if err := cn.nc.SetWriteDeadline(deadline(ctx, c.opts.WriteTimeout)); err != nil {
		return nil, err
	}
	if err := writeCommand(cn.bw, args...); err != nil {
		return nil, err
	}
	if err := cn.bw.Flush(); err != nil {
		return nil, err
	}
	if err := cn.nc.SetReadDeadline(deadline(ctx, c.opts.ReadTimeout)); err != nil {
		return nil, err
	}
	// Arming the read deadline can overwrite a cancel that already fired above,
	// so re-check before blocking: past this point only the deadline can wake
	// the read, and a cancel from here on is guaranteed to arrive after it.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reply, err := readReply(cn.br)
	if err != nil {
		return nil, err
	}
	if e, ok := reply.(Error); ok {
		return nil, e
	}
	return reply, nil
}

// getConn checks out a connection, reporting whether it came back from the idle
// list (reused) rather than being freshly dialled — the caller needs that to
// tell a stale parked socket from a genuine peer failure.
func (c *Client) getConn(ctx context.Context) (cn *conn, reused bool, err error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		<-c.sem
		return nil, false, errClosed
	}
	if n := len(c.idle); n > 0 {
		cn := c.idle[n-1]
		c.idle = c.idle[:n-1]
		c.mu.Unlock()
		return cn, true, nil
	}
	c.mu.Unlock()
	cn, err = c.dial(ctx)
	if err != nil {
		<-c.sem
		return nil, false, err
	}
	return cn, false, nil
}

// putConn returns a connection: parked for reuse when healthy, closed when the
// command errored (bad) or the client is closing. The token is always released.
func (c *Client) putConn(cn *conn, bad bool) {
	if bad {
		cn.close()
		<-c.sem
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cn.close()
		<-c.sem
		return
	}
	c.idle = append(c.idle, cn)
	c.mu.Unlock()
	<-c.sem
}

// Do runs one command and returns its reply.
func (c *Client) Do(ctx context.Context, args ...any) (any, error) {
	reply, retry, err := c.attempt(ctx, args...)
	// A connection parked in the pool can be closed by the peer at any time
	// (Redis `timeout`, a restart/failover, an LB or NAT reaping idle flows) and
	// nothing detects that before the write, so the first command after a quiet
	// period would fail on an already-dead socket. Retry such a failure exactly
	// once — attempt has discarded the stale connection, so the second try runs
	// on a healthy one. Never retry a ctx that is already done: the caller has
	// given up, and never retry a fresh connection, whose failure is the peer's.
	if retry && ctx.Err() == nil {
		reply, _, err = c.attempt(ctx, args...)
	}
	return reply, err
}

// attempt runs one command on one pooled connection and reports whether the
// failure is a candidate for a retry: a transport-level error (not a server
// Error) on a connection that came back from the idle list.
func (c *Client) attempt(ctx context.Context, args ...any) (any, bool, error) {
	cn, reused, err := c.getConn(ctx)
	if err != nil {
		return nil, false, err
	}
	reply, err := c.roundtrip(ctx, cn, args...)
	// A protocol/IO error (not a server Error) leaves the connection in an
	// unknown state, so discard it; a clean server Error keeps it reusable.
	var respErr Error
	bad := err != nil && !errors.As(err, &respErr)
	c.putConn(cn, bad)
	return reply, bad && reused, err
}

// Get returns the value of key, or ErrNil if it does not exist.
func (c *Client) Get(ctx context.Context, key string) (string, error) {
	reply, err := c.Do(ctx, "GET", key)
	if err != nil {
		return "", err
	}
	if reply == nil {
		return "", ErrNil
	}
	s, ok := reply.(string)
	if !ok {
		return "", fmt.Errorf("resp: unexpected %T reply from GET", reply)
	}
	return s, nil
}

// Set stores value under key. ttl > 0 sets a millisecond expiry (PX); ttl <= 0
// stores with no expiry.
func (c *Client) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if ttl > 0 {
		ms := ttl.Milliseconds()
		if ms <= 0 {
			ms = 1 // PX 0 is rejected; floor sub-ms TTLs to 1ms
		}
		_, err := c.Do(ctx, "SET", key, value, "PX", ms)
		return err
	}
	_, err := c.Do(ctx, "SET", key, value)
	return err
}

// Del deletes key and reports whether it existed.
func (c *Client) Del(ctx context.Context, key string) error {
	_, err := c.Do(ctx, "DEL", key)
	return err
}

// Ping checks liveness.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Do(ctx, "PING")
	return err
}

// Close closes all pooled connections. In-flight commands are not interrupted;
// their connections close when returned.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	idle := c.idle
	c.idle = nil
	c.mu.Unlock()
	for _, cn := range idle {
		cn.close()
	}
	return nil
}

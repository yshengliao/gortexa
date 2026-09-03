package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	apperr "github.com/yshengliao/gortexa/apperr"
	"github.com/yshengliao/gortexa/auth"
	"github.com/yshengliao/gortexa/config"
	"github.com/yshengliao/gortexa/interceptor"
)

const (
	protocolVersion = "2025-03-26"
	maxRequestBytes = 1 << 20
	// maxBatchElements caps a JSON-RPC batch's element count. The body cap alone
	// is not a bound on work: a 1 MiB body of `{}` entries is ~350k elements, and
	// every one of them is rejected before dispatch, so no gRPC Invoke happens and
	// load shedding, rate limiting and auth never see the request — the bridge
	// would still allocate a response per element and buffer the whole array.
	maxBatchElements = 100
	// bodyReadTimeout bounds a POST body read, restoring a slow-drip guard the
	// shared server's disabled ReadTimeout no longer provides; cleared before an
	// SSE response streams.
	bodyReadTimeout = 30 * time.Second
	// maxAuthzBytes bounds the inbound Authorization value the bridge is willing
	// to replay downstream. Unlike the body, a header is paid for once on the
	// wire but re-encoded onto one loopback Invoke per batch element (up to
	// maxBatchElements), so an unauthenticated caller can multiply header bytes
	// into ~100x the transport work before the auth stage rejects anything. A
	// real credential is a few KB at most and anything longer is already certain
	// to fail verification, so dropping it costs nothing legitimate.
	maxAuthzBytes = 8 << 10
)

// supportedProtocolVersions are the MCP revisions Gortexa can speak. On
// initialize the server echoes the client's requested version when it is one of
// these (the MCP spec's version negotiation), otherwise it offers its own
// default and lets the client decide whether to proceed.
var supportedProtocolVersions = map[string]bool{
	"2025-03-26": true,
	"2024-11-05": true,
}

// negotiateVersion picks the protocol version to advertise in the initialize
// result: the client's requested version if supported, else the server default.
func negotiateVersion(params json.RawMessage) string {
	if len(params) > 0 {
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if err := json.Unmarshal(params, &p); err == nil && supportedProtocolVersions[p.ProtocolVersion] {
			return p.ProtocolVersion
		}
	}
	return protocolVersion
}

// Bridge serves an MCP Streamable-HTTP endpoint backed by a gRPC loopback.
type Bridge struct {
	conn  *grpc.ClientConn
	reg   *apperr.Registry
	tools map[string]ToolIR
	order []string
	// mcpTools is the tools/list payload, downgraded once at construction. The
	// tool set is fixed for the bridge's lifetime, so tools/list returns this
	// cached slice instead of re-downgrading every schema per request.
	mcpTools []MCPTool
	observ   config.ObservConfig

	// allowedOrigins gates browser access for DNS-rebinding protection (MCP
	// Streamable HTTP): a browser request's Origin must be on this list.
	allowedOrigins map[string]struct{}
}

// NewBridge builds a bridge exposing the gortexa.ai.v1-annotated methods of the given
// services. tools/call dispatches over conn (which should be the in-process
// loopback so the full interceptor chain applies).
func NewBridge(conn *grpc.ClientConn, services []protoreflect.ServiceDescriptor, reg *apperr.Registry, observCfg ...config.ObservConfig) (*Bridge, error) {
	b := &Bridge{conn: conn, reg: reg, tools: map[string]ToolIR{}}
	if len(observCfg) > 0 {
		b.observ = observCfg[0]
	}
	for _, svc := range services {
		irs, err := BuildIR(svc)
		if err != nil {
			return nil, err
		}
		for _, ir := range irs {
			if _, dup := b.tools[ir.Name]; dup {
				return nil, fmt.Errorf("mcp: duplicate tool name %q across services", ir.Name)
			}
			b.tools[ir.Name] = ir
			b.order = append(b.order, ir.Name)
		}
	}
	sort.Strings(b.order)
	b.mcpTools = make([]MCPTool, 0, len(b.order))
	for _, name := range b.order {
		b.mcpTools = append(b.mcpTools, DowngradeMCP(b.tools[name]))
	}
	return b, nil
}

// ServiceDescriptors resolves service descriptors by full name from the global
// proto registry (e.g. "resource.v1.ResourceService").
func ServiceDescriptors(names ...protoreflect.FullName) ([]protoreflect.ServiceDescriptor, error) {
	out := make([]protoreflect.ServiceDescriptor, 0, len(names))
	for _, n := range names {
		d, err := protoregistry.GlobalFiles.FindDescriptorByName(n)
		if err != nil {
			return nil, fmt.Errorf("mcp: service %q not found: %w", n, err)
		}
		svc, ok := d.(protoreflect.ServiceDescriptor)
		if !ok {
			return nil, fmt.Errorf("mcp: %q is not a service", n)
		}
		out = append(out, svc)
	}
	return out, nil
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// SetAllowedOrigins configures the browser-origin allowlist used for the MCP
// Streamable-HTTP DNS-rebinding protection. Pass the server's CORS origins. A
// request carrying an Origin not on the list is rejected with 403; requests
// without an Origin header (non-browser clients like the MCP SDK or curl) are
// always allowed.
//
// A "*" entry is deliberately ignored here, NOT treated as allow-all: a wildcard
// is safe for CORS (no-credentials reflection) but would defeat DNS-rebinding
// protection, letting any website drive the MCP endpoint through a victim's
// browser. To permit a browser origin, list it explicitly.
func (b *Bridge) SetAllowedOrigins(origins []string) {
	b.allowedOrigins = make(map[string]struct{}, len(origins))
	for _, o := range origins {
		if o == "*" {
			continue
		}
		b.allowedOrigins[o] = struct{}{}
	}
}

// originAllowed implements the DNS-rebinding guard: absent Origin (non-browser)
// is allowed; otherwise the origin must be explicitly allowlisted.
func (b *Bridge) originAllowed(origin string) bool {
	if origin == "" {
		return true
	}
	_, ok := b.allowedOrigins[origin]
	return ok
}

// Handler returns the Streamable-HTTP MCP handler (mounted at /mcp).
func (b *Bridge) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// DNS-rebinding protection: validate Origin before touching the body.
		if !b.originAllowed(r.Header.Get("Origin")) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodPost:
			b.servePost(w, r)
		case http.MethodGet:
			b.handleGet(w, r)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// servePost runs handlePost under panic recovery so a panic in dispatch/toolsCall
// (protojson, dynamicpb, etc.) becomes a clean JSON-RPC -32603 instead of an
// abruptly dropped connection — mirroring the gRPC Recovery interceptor for the
// HTTP/JSON-RPC surface. A panic occurs before writeRPC runs, so no response has
// been written yet.
func (b *Bridge) servePost(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			writeRPC(w, r, rpcResponse{
				JSONRPC: "2.0",
				ID:      json.RawMessage("null"),
				Error:   &rpcError{Code: -32603, Message: "internal error"},
			})
		}
	}()
	b.handlePost(w, r)
}

func (b *Bridge) handlePost(w http.ResponseWriter, r *http.Request) {
	if !isSupportedJSONContentType(r.Header.Get("Content-Type")) {
		http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
		return
	}
	// Bound the body read: the shared server runs with ReadTimeout disabled (so
	// it can't cut off SSE streams), so a slow-drip POST would otherwise hold the
	// connection open indefinitely. The deadline is cleared before any streaming
	// response begins.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(bodyReadTimeout))
	// Read one byte past the cap so an oversized body is reported as 413 rather
	// than being silently truncated into a parse error.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	// Clear on every path — before the error response and before an SSE stream —
	// so a stale read deadline can't bleed onto a reused keep-alive connection.
	_ = rc.SetReadDeadline(time.Time{})
	if err != nil {
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	if len(body) > maxRequestBytes {
		writeRPCStatus(w, r, http.StatusRequestEntityTooLarge,
			rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32000, Message: "request entity too large"}})
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}

	switch body[0] {
	case '[':
		b.handleBatchPost(w, r, body)
	case '{':
		b.handleSinglePost(w, r, body)
	default:
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32600, Message: "invalid request"}})
	}
}

func (b *Bridge) handleSinglePost(w http.ResponseWriter, r *http.Request, body []byte) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	req, id, rerr := validateRPCRequest(fields)
	if rerr != nil {
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", ID: id, Error: rerr})
		return
	}

	if req.Method == "initialize" {
		w.Header().Set("Mcp-Session-Id", newSessionID())
	}

	// Notifications (no id) get acknowledged with 202 and no body.
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPC(w, r, b.dispatch(r, req))
}

// handleBatchPost dispatches a JSON-RPC batch (an array of requests). Each entry
// is validated and dispatched independently; notification entries (no id) yield
// no response, an empty array is rejected with -32600, and an all-notification
// batch is acknowledged with 202.
func (b *Bridge) handleBatchPost(w http.ResponseWriter, r *http.Request, body []byte) {
	var raws []json.RawMessage
	if err := json.Unmarshal(body, &raws); err != nil {
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	if len(raws) == 0 {
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32600, Message: "invalid request"}})
		return
	}
	// Reject before allocating anything per element.
	if len(raws) > maxBatchElements {
		writeRPCStatus(w, r, http.StatusRequestEntityTooLarge,
			rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32000, Message: "batch too large"}})
		return
	}

	responses := make([]rpcResponse, 0, len(raws))
	for _, raw := range raws {
		raw = bytes.TrimSpace(raw)
		var fields map[string]json.RawMessage
		if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &fields) != nil {
			responses = append(responses, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32600, Message: "invalid request"}})
			continue
		}
		req, id, rerr := validateRPCRequest(fields)
		if rerr != nil {
			responses = append(responses, rpcResponse{JSONRPC: "2.0", ID: id, Error: rerr})
			continue
		}
		if req.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", newSessionID())
		}
		if len(req.ID) == 0 {
			continue // notification: no response
		}
		responses = append(responses, b.dispatch(r, req))
	}

	if len(responses) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPCValue(w, r, http.StatusOK, responses)
}

// validateRPCRequest enforces the JSON-RPC 2.0 request shape, returning a
// -32600 (invalid request) error with the request id (or null when it can't be
// determined) for malformed payloads.
func validateRPCRequest(fields map[string]json.RawMessage) (rpcRequest, json.RawMessage, *rpcError) {
	invalid := &rpcError{Code: -32600, Message: "invalid request"}
	nullID := json.RawMessage("null")

	jsonrpc, ok := fields["jsonrpc"]
	if !ok || string(jsonrpc) != `"2.0"` {
		return rpcRequest{}, requestID(fields, nullID), invalid
	}

	methodRaw, ok := fields["method"]
	if !ok {
		return rpcRequest{}, requestID(fields, nullID), invalid
	}
	var method string
	if err := json.Unmarshal(methodRaw, &method); err != nil {
		return rpcRequest{}, requestID(fields, nullID), invalid
	}

	id := json.RawMessage(nil)
	if raw, ok := fields["id"]; ok {
		if !validRPCID(raw) {
			return rpcRequest{}, nullID, invalid
		}
		id = raw
	}

	params := json.RawMessage(nil)
	if raw, ok := fields["params"]; ok {
		if !validRPCParams(method, raw) {
			return rpcRequest{}, idOrNull(id), invalid
		}
		params = raw
	}

	return rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}, id, nil
}

func requestID(fields map[string]json.RawMessage, fallback json.RawMessage) json.RawMessage {
	if raw, ok := fields["id"]; ok && validRPCID(raw) {
		return raw
	}
	return fallback
}

func idOrNull(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

// maxRPCIDNumberLen bounds a numeric JSON-RPC id's text length. Real ids are
// small; the cap (with the fraction/exponent rejection below) keeps id
// validation from doing unbounded work on attacker input.
const maxRPCIDNumberLen = 32

// validRPCID accepts null, a string, or an integer-valued JSON number (JSON-RPC
// 2.0 disallows fractional ids).
func validRPCID(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return true
	}
	if len(trimmed) == 0 {
		return false
	}
	switch trimmed[0] {
	case '"':
		var s string
		return json.Unmarshal(trimmed, &s) == nil
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		// Validate syntactically. Never expand the number via big.Rat.SetString:
		// a decimal-exponent literal like "1e999999999" would balloon into a
		// giant integer, an unauthenticated CPU/memory amplification DoS.
		if len(trimmed) > maxRPCIDNumberLen {
			return false
		}
		var n json.Number
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		if err := dec.Decode(&n); err != nil {
			return false
		}
		if err := dec.Decode(&struct{}{}); err != io.EOF {
			return false
		}
		// json.Number preserves the source text; an integer id has no fraction or
		// exponent. This rejects "1.5" and "1e3" without any arithmetic.
		return !strings.ContainsAny(n.String(), ".eE")
	default:
		return false
	}
}

func validRPCParams(_ string, raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	// JSON-RPC 2.0: params, if present, MUST be a structured value (object or
	// array). Whether a specific method needs an object (e.g. tools/call) is a
	// params-level concern resolved at dispatch as -32602 — not a request-shape
	// (-32600) error, and never an error reply to a notification.
	return (trimmed[0] == '{' || trimmed[0] == '[') && json.Valid(trimmed)
}

// ssePingInterval bounds how long an idle SSE stream sits silent; a periodic
// comment frame keeps intermediaries (proxies, load balancers) from dropping it.
const ssePingInterval = 25 * time.Second

func (b *Bridge) handleGet(w http.ResponseWriter, r *http.Request) {
	// A bare GET opens an SSE stream for server→client messages. Gortexa emits
	// none today, so it stays open (with keep-alives) until the client leaves.
	// Clear any per-stream write deadline so a configured http.Server WriteTimeout
	// can't kill this long-lived stream before the first keep-alive ping fires.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flush(w)

	ticker := time.NewTicker(ssePingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flush(w)
		}
	}
}

func (b *Bridge) dispatch(r *http.Request, req rpcRequest) rpcResponse {
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": negotiateVersion(req.Params),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "gortexa", "version": "0.1.0"},
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": b.mcpTools}
	case "tools/call":
		result, rerr := b.toolsCall(r, req.ID, req.Params)
		if rerr != nil {
			resp.Error = rerr
		} else {
			resp.Result = result
		}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp
}

// inboundContext builds the dispatch context from an inbound /mcp request. It
// continues W3C trace context but strips inbound baggage: /mcp is a public
// surface and baggage members are attacker-controlled, so they must not
// propagate over the loopback as trusted context (tenant/user hints and the
// like). This mirrors the existing distrust of client X-Forwarded-For.
func inboundContext(r *http.Request) context.Context {
	ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	return baggage.ContextWithoutBaggage(ctx)
}

func (b *Bridge) toolsCall(r *http.Request, id json.RawMessage, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params"}
	}
	tool, ok := b.tools[p.Name]
	if !ok {
		return nil, &rpcError{Code: -32602, Message: "unknown tool: " + p.Name}
	}

	in := dynamicpb.NewMessage(tool.Input)
	if len(p.Arguments) > 0 {
		if err := protojson.Unmarshal(p.Arguments, in); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid arguments: " + err.Error()}
		}
	}
	out := dynamicpb.NewMessage(tool.Output)

	ctx := inboundContext(r)
	attrs := []attribute.KeyValue{attribute.String("gen_ai.tool.name", p.Name), attribute.String("mcp.method.name", "tools/call"), attribute.String("mcp.protocol.version", protocolVersion), attribute.String("jsonrpc.request.id", string(id))}
	if sid := r.Header.Get("Mcp-Session-Id"); sid != "" {
		attrs = append(attrs, attribute.String("mcp.session.id", sid))
	}
	ctx, span := otel.Tracer("github.com/yshengliao/gortexa/mcp").Start(ctx, "execute_tool "+p.Name, trace.WithSpanKind(trace.SpanKindInternal), trace.WithAttributes(attrs...))
	defer span.End()
	if b.observ.GenAICaptureContent {
		span.SetAttributes(attribute.String("gen_ai.tool.call.arguments", MaskSecrets(string(p.Arguments), b.observ.GenAIMaskFields)))
	}
	md := metadata.MD{}
	// Both forwards are filtered: net/http accepts header bytes (HTAB, anything
	// >0x7E) that gRPC's outgoing-metadata validator rejects, and that rejection
	// fails the call with codes.Internal before it leaves the process — so a stray
	// byte in either header would mask the answer the chain owes the caller
	// (Unauthenticated for a bad credential) with an opaque internal error.
	if authz := r.Header.Get("Authorization"); authz != "" && len(authz) <= maxAuthzBytes && printableASCII(authz) {
		md.Set(auth.MetadataKey, authz)
	}
	// An invalid id is dropped, not propagated, matching the gateway path and the
	// request-id interceptor: a fresh one is minted downstream.
	if rid := r.Header.Get("X-Request-Id"); interceptor.ValidRequestID(rid) {
		md.Set(interceptor.RequestIDMetadataKey, rid)
	}
	// Carry the real client IP across the loopback so the rate limiter keys on
	// the caller, not the shared "bufconn" peer. Derived from our own RemoteAddr,
	// never a client-supplied header. Matches interceptor.PeerIPMetaKey.
	if ip := clientIP(r); ip != "" {
		md.Set(interceptor.PeerIPMetaKey, ip)
	}
	if len(md) > 0 {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	if err := b.conn.Invoke(ctx, tool.FullMethod, in, out); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return b.toolError(err), nil // tool errors are results with isError:true, not RPC errors
	}
	js, err := protojson.Marshal(out)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return b.toolError(apperr.New(apperr.CatInternal, "marshal output")), nil
	}
	if b.observ.GenAICaptureContent {
		span.SetAttributes(attribute.String("gen_ai.tool.call.result", MaskSecrets(string(js), b.observ.GenAIMaskFields)))
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(js)}},
		"isError": false,
	}, nil
}

// printableASCII reports whether every byte is in [0x20,0x7E], the range gRPC
// requires of outgoing metadata values.
func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7E {
			return false
		}
	}
	return true
}

func (b *Bridge) toolError(err error) map[string]any {
	me := b.reg.ToMCP(err)
	return map[string]any{
		"content":       []map[string]any{{"type": "text", "text": me.Message}},
		"isError":       true,
		"errorCategory": me.ErrorCategory,
		"isRetryable":   me.IsRetryable,
	}
}

func writeRPC(w http.ResponseWriter, r *http.Request, resp rpcResponse) {
	writeRPCStatus(w, r, http.StatusOK, resp)
}

func writeRPCStatus(w http.ResponseWriter, r *http.Request, status int, resp rpcResponse) {
	writeRPCValue(w, r, status, resp)
}

// writeRPCValue renders a JSON-RPC response (a single object or a batch array),
// honoring the client's Accept header: SSE when requested, JSON otherwise, and
// 406 when neither is acceptable.
func writeRPCValue(w http.ResponseWriter, r *http.Request, status int, value any) {
	responseType, ok := negotiateResponseType(r.Header.Get("Accept"))
	if !ok {
		http.Error(w, "not acceptable", http.StatusNotAcceptable)
		return
	}
	buf, err := json.Marshal(value)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if responseType == "text/event-stream" {
		w.Header().Set("Content-Type", "text/event-stream")
		// Honor the caller-supplied status (e.g. 413 for an oversized body) rather
		// than downgrading every SSE response to 200.
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", buf)
		flush(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf)
}

// isSupportedJSONContentType requires POST bodies to be JSON (application/json
// or a +json media type) so the bridge never guesses at non-JSON payloads.
func isSupportedJSONContentType(contentType string) bool {
	if contentType == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

// negotiateResponseType chooses the response media type from the Accept header,
// honoring q-values (q=0 marks a type unacceptable) and preferring SSE when the
// client explicitly accepts it. An empty Accept defaults to JSON.
func negotiateResponseType(accept string) (string, bool) {
	if strings.TrimSpace(accept) == "" {
		return "application/json", true
	}

	jsonAcceptable := false
	eventStreamAcceptable := false
	for part := range strings.SplitSeq(accept, ",") {
		mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		if q, ok := params["q"]; ok {
			quality, err := strconv.ParseFloat(q, 64)
			if err != nil || quality <= 0 {
				continue
			}
		}
		switch strings.ToLower(mediaType) {
		case "text/event-stream", "text/*":
			eventStreamAcceptable = true
		case "application/json", "application/*", "*/*":
			jsonAcceptable = true
		default:
			if strings.HasSuffix(strings.ToLower(mediaType), "+json") {
				jsonAcceptable = true
			}
		}
	}

	if eventStreamAcceptable {
		return "text/event-stream", true
	}
	if jsonAcceptable {
		return "application/json", true
	}
	return "", false
}

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// clientIP returns the host portion of the request's remote address.
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host
}

package mcp

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"mime"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/yshengliao/gortexa/internal/auth"
	"github.com/yshengliao/gortexa/internal/config"
	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/interceptor"
)

const (
	protocolVersion = "2025-03-26"
	maxRequestBytes = 1 << 20
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
}

// NewBridge builds a bridge exposing the ai.v1-annotated methods of the given
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

// Handler returns the Streamable-HTTP MCP handler (mounted at /mcp).
func (b *Bridge) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			b.servePost(w, r)
		case http.MethodGet:
			b.handleGet(w, r)
		default:
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
	// Read one byte past the cap so an oversized body is reported as 413 rather
	// than being silently truncated into a parse error.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil {
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	if len(body) > maxRequestBytes {
		writeRPCStatus(w, r, http.StatusRequestEntityTooLarge,
			rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32000, Message: "request entity too large"}})
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
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
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
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
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	if len(raws) == 0 {
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32600, Message: "invalid request"}})
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
		var n json.Number
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		if err := dec.Decode(&n); err != nil {
			return false
		}
		if err := dec.Decode(&struct{}{}); err != io.EOF {
			return false
		}
		rat, ok := new(big.Rat).SetString(n.String())
		return ok && rat.IsInt()
	default:
		return false
	}
}

func validRPCParams(method string, raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	switch method {
	case "tools/call":
		return trimmed[0] == '{' && json.Valid(trimmed)
	default:
		return (trimmed[0] == '{' || trimmed[0] == '[') && json.Valid(trimmed)
	}
}

// ssePingInterval bounds how long an idle SSE stream sits silent; a periodic
// comment frame keeps intermediaries (proxies, load balancers) from dropping it.
const ssePingInterval = 25 * time.Second

func (b *Bridge) handleGet(w http.ResponseWriter, r *http.Request) {
	// A bare GET opens an SSE stream for server→client messages. Gortexa emits
	// none today, so it stays open (with keep-alives) until the client leaves.
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

	ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	attrs := []attribute.KeyValue{attribute.String("gen_ai.tool.name", p.Name), attribute.String("mcp.method.name", "tools/call"), attribute.String("mcp.protocol.version", protocolVersion), attribute.String("jsonrpc.request.id", string(id))}
	if sid := r.Header.Get("Mcp-Session-Id"); sid != "" {
		attrs = append(attrs, attribute.String("mcp.session.id", sid))
	}
	ctx, span := otel.Tracer("github.com/yshengliao/gortexa/internal/mcp").Start(ctx, "execute_tool "+p.Name, trace.WithSpanKind(trace.SpanKindInternal), trace.WithAttributes(attrs...))
	defer span.End()
	if b.observ.GenAICaptureContent {
		span.SetAttributes(attribute.String("gen_ai.tool.call.arguments", MaskSecrets(string(p.Arguments), b.observ.GenAIMaskFields)))
	}
	md := metadata.MD{}
	if authz := r.Header.Get("Authorization"); authz != "" {
		md.Set(auth.MetadataKey, authz)
	}
	if rid := r.Header.Get("X-Request-Id"); rid != "" {
		md.Set("x-request-id", rid) // matches interceptor.RequestIDMetadataKey
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
		w.WriteHeader(http.StatusOK)
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
		case "text/event-stream":
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

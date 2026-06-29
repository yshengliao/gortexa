package mcp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/yshengliao/gortexa/internal/auth"
	apperr "github.com/yshengliao/gortexa/internal/errors"
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
}

// NewBridge builds a bridge exposing the ai.v1-annotated methods of the given
// services. tools/call dispatches over conn (which should be the in-process
// loopback so the full interceptor chain applies).
func NewBridge(conn *grpc.ClientConn, services []protoreflect.ServiceDescriptor, reg *apperr.Registry) (*Bridge, error) {
	b := &Bridge{conn: conn, reg: reg, tools: map[string]ToolIR{}}
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
			b.handlePost(w, r)
		case http.MethodGet:
			b.handleGet(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func (b *Bridge) handlePost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
	if err != nil {
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
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

func (b *Bridge) handleGet(w http.ResponseWriter, r *http.Request) {
	// A bare GET opens an SSE stream for server→client messages. Gortexa emits
	// none today, so it stays open until the client disconnects.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	<-r.Context().Done()
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
		tools := make([]MCPTool, 0, len(b.order))
		for _, name := range b.order {
			tools = append(tools, DowngradeMCP(b.tools[name]))
		}
		resp.Result = map[string]any{"tools": tools}
	case "tools/call":
		result, rerr := b.toolsCall(r, req.Params)
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

func (b *Bridge) toolsCall(r *http.Request, params json.RawMessage) (any, *rpcError) {
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

	ctx := r.Context()
	md := metadata.MD{}
	if authz := r.Header.Get("Authorization"); authz != "" {
		md.Set(auth.MetadataKey, authz)
	}
	if rid := r.Header.Get("X-Request-Id"); rid != "" {
		md.Set("x-request-id", rid) // matches interceptor.RequestIDMetadataKey
	}
	// Carry the real client IP across the loopback so the rate limiter keys on
	// the caller, not the shared "bufconn" peer. Derived from our own RemoteAddr,
	// never a client-supplied header. Matches interceptor.ForwardedForMetaKey.
	if ip := clientIP(r); ip != "" {
		md.Set("x-forwarded-for", ip)
	}
	if len(md) > 0 {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	if err := b.conn.Invoke(ctx, tool.FullMethod, in, out); err != nil {
		return b.toolError(err), nil // tool errors are results with isError:true, not RPC errors
	}
	js, err := protojson.Marshal(out)
	if err != nil {
		return b.toolError(apperr.New(apperr.CatInternal, "marshal output")), nil
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
	buf, err := json.Marshal(resp)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", buf)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
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

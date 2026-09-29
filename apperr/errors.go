// Package apperr is Gortexa's central error model. A single table of Mapping
// rows is the one source of truth that drives all three transports: gRPC
// status codes, HTTP status codes (for the grpc-gateway error handler), and
// MCP tool-call error envelopes. Domain code constructs *Error values with a
// Category; the adapters translate them per transport. A hard invariant holds
// everywhere: an Internal error (or any non-*Error) never serializes its
// underlying cause — only the category's SafeMessage is exposed.
package apperr

import (
	"context"
	stderrors "errors"
	"fmt"
	"maps"
	"net/http"
	"sync"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Category is a transport-agnostic error class.
type Category string

const (
	CatInvalidArgument    Category = "invalid_argument"
	CatUnauthenticated    Category = "unauthenticated"
	CatPermissionDenied   Category = "permission_denied"
	CatNotFound           Category = "not_found"
	CatAlreadyExists      Category = "already_exists"
	CatResourceExhausted  Category = "resource_exhausted"
	CatFailedPrecondition Category = "failed_precondition"
	CatAborted            Category = "aborted"
	CatDeadlineExceeded   Category = "deadline_exceeded"
	CatUnavailable        Category = "unavailable"
	CatUnimplemented      Category = "unimplemented"
	CatInternal           Category = "internal"
	CatCanceled           Category = "canceled"
	CatOutOfRange         Category = "out_of_range"
	CatDataLoss           Category = "data_loss"
)

// statusClientClosedRequest is the (non-standard) HTTP status used by gateways
// for a client-canceled request; net/http has no constant for it.
const statusClientClosedRequest = 499

// Mapping is one row of the error table.
type Mapping struct {
	Category    Category
	GRPCCode    codes.Code
	HTTPStatus  int
	Retryable   bool
	SafeMessage string
}

// DefaultMappings returns the seed error table.
func DefaultMappings() []Mapping {
	return []Mapping{
		{CatInvalidArgument, codes.InvalidArgument, http.StatusBadRequest, false, "invalid argument"},
		{CatUnauthenticated, codes.Unauthenticated, http.StatusUnauthorized, false, "unauthenticated"},
		{CatPermissionDenied, codes.PermissionDenied, http.StatusForbidden, false, "permission denied"},
		{CatNotFound, codes.NotFound, http.StatusNotFound, false, "not found"},
		{CatAlreadyExists, codes.AlreadyExists, http.StatusConflict, false, "already exists"},
		{CatResourceExhausted, codes.ResourceExhausted, http.StatusTooManyRequests, true, "resource exhausted"},
		{CatFailedPrecondition, codes.FailedPrecondition, http.StatusPreconditionFailed, false, "failed precondition"},
		{CatAborted, codes.Aborted, http.StatusConflict, true, "aborted"},
		{CatDeadlineExceeded, codes.DeadlineExceeded, http.StatusGatewayTimeout, true, "deadline exceeded"},
		{CatUnavailable, codes.Unavailable, http.StatusServiceUnavailable, true, "unavailable"},
		{CatUnimplemented, codes.Unimplemented, http.StatusNotImplemented, false, "unimplemented"},
		{CatInternal, codes.Internal, http.StatusInternalServerError, false, "internal error"},
		// Codes that previously fell through to Internal/500.
		{CatCanceled, codes.Canceled, statusClientClosedRequest, false, "canceled"},
		{CatOutOfRange, codes.OutOfRange, http.StatusBadRequest, false, "out of range"},
		{CatDataLoss, codes.DataLoss, http.StatusInternalServerError, false, "data loss"},
	}
}

// Error is a domain error carrying a Category, a client-safe message and an
// optional wrapped cause (never exposed for Internal).
type Error struct {
	Category Category
	Msg      string
	cause    error
	fields   map[string]any
}

// New creates an Error with no cause.
func New(cat Category, msg string) *Error { return &Error{Category: cat, Msg: msg} }

// Wrap creates an Error wrapping cause.
func Wrap(cat Category, msg string, cause error) *Error {
	return &Error{Category: cat, Msg: msg, cause: cause}
}

// With returns a copy of e with a structured field attached (for logging, never
// serialized to clients). e itself is left untouched, so With is safe on a
// shared sentinel such as a package-level ErrX.
func (e *Error) With(key string, val any) *Error {
	c := *e
	c.fields = make(map[string]any, len(e.fields)+1)
	maps.Copy(c.fields, e.fields)
	c.fields[key] = val
	return &c
}

// Fields returns the attached structured fields.
func (e *Error) Fields() map[string]any { return e.fields }

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Category, e.Msg, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Category, e.Msg)
}

// Unwrap exposes the cause to errors.Is/As. A typed-nil *Error unwraps to nil.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// GRPCStatus lets *Error satisfy the gRPC status interface so handlers may
// return it directly. Uses the default registry.
func (e *Error) GRPCStatus() *status.Status {
	if e == nil {
		return status.New(codes.Internal, Default.internal().SafeMessage)
	}
	return Default.ToGRPCStatus(e)
}

// Registry maps categories to transport codes. Safe for concurrent use.
type Registry struct {
	mu     sync.RWMutex
	byCat  map[Category]Mapping
	byCode map[codes.Code]Category
}

// NewRegistry builds a registry seeded with the given mappings.
func NewRegistry(seed ...Mapping) *Registry {
	r := &Registry{byCat: make(map[Category]Mapping), byCode: make(map[codes.Code]Category)}
	for _, m := range seed {
		r.Register(m)
	}
	return r
}

// errorInfoDomain scopes the ErrorInfo detail that carries a category across
// the gRPC loopback; details from any other domain are ignored.
const errorInfoDomain = "gortexa.apperr"

// Register adds a mapping, panicking (fail-loud at startup) on a duplicate
// category or on codes.OK / codes.Unknown: an OK status is not an error (its
// Err() is nil, so the failure would be swallowed) and Unknown is what an
// unmapped error looks like on the wire.
//
// A gRPC code may be shared. The first category registered for a code owns it:
// a bare status with that code (e.g. from a downstream call) resolves to the
// owner. Every other category on the code — in practice any custom category,
// since DefaultMappings claims all remaining codes — is carried across the
// loopback as an ErrorInfo{Domain: errorInfoDomain, Reason: category} status
// detail, so the gateway and MCP bridge still see the exact category. Only the
// category name travels, never a message or cause.
func (r *Registry) Register(m Mapping) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byCat[m.Category]; dup {
		panic("apperr: duplicate mapping for category " + string(m.Category))
	}
	if m.GRPCCode == codes.OK || m.GRPCCode == codes.Unknown {
		panic(fmt.Sprintf("apperr: category %q cannot map to gRPC code %v", m.Category, m.GRPCCode))
	}
	r.byCat[m.Category] = m
	if _, taken := r.byCode[m.GRPCCode]; !taken {
		r.byCode[m.GRPCCode] = m.Category
	}
}

// Lookup returns the mapping for a category.
func (r *Registry) Lookup(c Category) (Mapping, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.byCat[c]
	return m, ok
}

func (r *Registry) internal() Mapping {
	if m, ok := r.Lookup(CatInternal); ok {
		return m
	}
	return Mapping{CatInternal, codes.Internal, http.StatusInternalServerError, false, "internal error"}
}

// resolve reduces any error to its mapping and the client-safe message to emit.
// Internal (and any unrecognized error) yields only the SafeMessage.
func (r *Registry) resolve(err error) (Mapping, string) {
	if err == nil {
		m, _ := r.Lookup(CatInternal)
		return m, ""
	}
	if e, isErr := stderrors.AsType[*Error](err); isErr && e != nil {
		m, ok := r.Lookup(e.Category)
		if !ok || m.Category == CatInternal {
			in := r.internal()
			return in, in.SafeMessage
		}
		if m.Category != CatInvalidArgument && m.Category != CatUnauthenticated {
			return m, m.SafeMessage
		}
		msg := e.Msg
		if msg == "" {
			msg = m.SafeMessage
		}
		return m, msg
	}
	// A context error carries no status; without this it would become Internal.
	if cat, isCtx := contextCategory(err); isCtx {
		if m, ok := r.Lookup(cat); ok {
			return m, m.SafeMessage
		}
	}
	// Pre-built gRPC status (e.g. from a downstream call) passes through by its
	// category detail, else by code.
	if s, ok := status.FromError(err); ok && s.Code() != codes.Unknown && s.Code() != codes.OK {
		if cat, found := r.statusCategory(s); found {
			if m, ok := r.Lookup(cat); ok && m.Category != CatInternal {
				if m.Category == CatInvalidArgument || m.Category == CatUnauthenticated {
					// status.FromError splices the whole %w chain into the
					// message when err merely wraps a status, so only the
					// innermost status's own message may be forwarded: a
					// handler's wrapper text is never client-facing, the same
					// rule the *Error branch above applies by emitting e.Msg
					// alone.
					if gs, direct := stderrors.AsType[interface {
						error
						GRPCStatus() *status.Status
					}](err); direct {
						if inner := gs.GRPCStatus(); inner != nil {
							return m, inner.Message()
						}
					}
					return m, m.SafeMessage
				}
				return m, m.SafeMessage
			}
		}
	}
	in := r.internal()
	return in, in.SafeMessage
}

func contextCategory(err error) (Category, bool) {
	switch {
	case stderrors.Is(err, context.DeadlineExceeded):
		return CatDeadlineExceeded, true
	case stderrors.Is(err, context.Canceled):
		return CatCanceled, true
	}
	return "", false
}

// statusCategory recovers the category of a status: an ErrorInfo detail from
// ToGRPCStatus names it (honoured only when it agrees with the status code),
// otherwise the code's owning category.
func (r *Registry) statusCategory(s *status.Status) (Category, bool) {
	for _, d := range s.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == errorInfoDomain {
			if m, ok := r.Lookup(Category(info.GetReason())); ok && m.GRPCCode == s.Code() {
				return m.Category, true
			}
		}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	cat, found := r.byCode[s.Code()]
	return cat, found
}

// ToGRPCStatus maps any error to a gRPC status. A nil error maps to OK. A
// category that does not own its code gets an ErrorInfo detail naming it (see
// Register).
func (r *Registry) ToGRPCStatus(err error) *status.Status {
	if err == nil {
		return status.New(codes.OK, "")
	}
	m, msg := r.resolve(err)
	st := status.New(m.GRPCCode, msg)
	r.mu.RLock()
	owner := r.byCode[m.GRPCCode]
	r.mu.RUnlock()
	if owner == "" || owner == m.Category {
		return st
	}
	if withInfo, derr := st.WithDetails(&errdetails.ErrorInfo{Reason: string(m.Category), Domain: errorInfoDomain}); derr == nil {
		return withInfo
	}
	return st
}

// ToHTTP maps any error to an HTTP status and a client-safe body.
func (r *Registry) ToHTTP(err error) (int, ErrorBody) {
	m, msg := r.resolve(err)
	return m.HTTPStatus, ErrorBody{Code: string(m.Category), Message: msg}
}

// ToMCP maps any error to an MCP tool-call error envelope.
func (r *Registry) ToMCP(err error) MCPError {
	m, msg := r.resolve(err)
	return MCPError{IsError: true, ErrorCategory: string(m.Category), IsRetryable: m.Retryable, Message: msg}
}

// ErrorBody is the JSON body emitted by the HTTP gateway error handler.
type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// MCPError is embedded in an MCP tools/call result on failure.
type MCPError struct {
	IsError       bool   `json:"isError"`
	ErrorCategory string `json:"errorCategory"`
	IsRetryable   bool   `json:"isRetryable"`
	Message       string `json:"message"`
}

// Default is the process-wide registry seeded with DefaultMappings.
var Default = NewRegistry(DefaultMappings()...)

// Package-level convenience wrappers over Default.

func Register(m Mapping) { Default.Register(m) }

func Lookup(c Category) (Mapping, bool) { return Default.Lookup(c) }

func ToGRPCStatus(err error) *status.Status { return Default.ToGRPCStatus(err) }

func ToHTTP(err error) (int, ErrorBody) { return Default.ToHTTP(err) }

func ToMCP(err error) MCPError { return Default.ToMCP(err) }

// Is reports whether err is an *Error with the given category.
func Is(err error, cat Category) bool {
	if e, ok := stderrors.AsType[*Error](err); ok && e != nil {
		return e.Category == cat
	}
	return false
}

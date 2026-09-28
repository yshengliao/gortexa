package apperr_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apperr "github.com/yshengliao/gortexa/apperr"
)

// A custom category must survive the gRPC loopback: Recovery turns the *Error
// into a status, and the gateway / MCP bridge only ever see that status.
func TestCustomCategorySurvivesLoopback(t *testing.T) {
	const teapot = apperr.Category("teapot")
	r := apperr.NewRegistry(apperr.DefaultMappings()...)
	r.Register(apperr.Mapping{Category: teapot, GRPCCode: codes.FailedPrecondition, HTTPStatus: 418, Retryable: true, SafeMessage: "short and stout"})

	wire := r.ToGRPCStatus(apperr.Wrap(teapot, "handler detail", fmt.Errorf("db secret"))).Err()
	for name, err := range map[string]error{"bare": wire, "wrapped": fmt.Errorf("call: %w", wire)} {
		t.Run(name, func(t *testing.T) {
			code, body := r.ToHTTP(err)
			if code != 418 || body.Code != string(teapot) || body.Message != "short and stout" {
				t.Fatalf("ToHTTP = %d %+v, want 418 teapot safe message", code, body)
			}
			if m := r.ToMCP(err); m.ErrorCategory != string(teapot) || !m.IsRetryable {
				t.Fatalf("ToMCP = %+v, want teapot retryable", m)
			}
			if st := r.ToGRPCStatus(err); st.Code() != codes.FailedPrecondition || st.Message() != "short and stout" {
				t.Fatalf("ToGRPCStatus = %v %q", st.Code(), st.Message())
			}
		})
	}

	// The code's owner travels without a detail and is unaffected.
	st := r.ToGRPCStatus(apperr.New(apperr.CatFailedPrecondition, "x"))
	if len(st.Details()) != 0 {
		t.Fatalf("owner status carries details %v", st.Details())
	}
	if _, body := r.ToHTTP(st.Err()); body.Code != string(apperr.CatFailedPrecondition) {
		t.Fatalf("owner resolved to %q", body.Code)
	}
}

// Only our own domain, naming a registered category on its own code, is
// honoured; anything else falls back to the code's owner.
func TestErrorInfoDetailIgnoredWhenForeignOrMismatched(t *testing.T) {
	r := apperr.NewRegistry(apperr.DefaultMappings()...)
	r.Register(apperr.Mapping{Category: "teapot", GRPCCode: codes.FailedPrecondition, HTTPStatus: 418, SafeMessage: "tea"})
	cases := map[string]*errdetails.ErrorInfo{
		"foreign domain": {Domain: "example.com", Reason: "teapot"},
		"code mismatch":  {Domain: "gortexa.apperr", Reason: string(apperr.CatInvalidArgument)},
		"unknown reason": {Domain: "gortexa.apperr", Reason: "nope"},
	}
	for name, info := range cases {
		t.Run(name, func(t *testing.T) {
			st, err := status.New(codes.FailedPrecondition, "downstream text").WithDetails(info)
			if err != nil {
				t.Fatal(err)
			}
			_, body := r.ToHTTP(st.Err())
			if body.Code != string(apperr.CatFailedPrecondition) || body.Message != "failed precondition" {
				t.Fatalf("ToHTTP body = %+v, want failed_precondition safe message", body)
			}
		})
	}
}

func TestRegisterRejectsOKAndUnknown(t *testing.T) {
	for _, c := range []codes.Code{codes.OK, codes.Unknown} {
		t.Run(c.String(), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("Register with %v did not panic", c)
				}
			}()
			apperr.NewRegistry().Register(apperr.Mapping{Category: "custom", GRPCCode: c})
		})
	}
}

func TestContextErrorsMapToTheirCategories(t *testing.T) {
	cases := []struct {
		err  error
		cat  apperr.Category
		code codes.Code
		http int
	}{
		{context.DeadlineExceeded, apperr.CatDeadlineExceeded, codes.DeadlineExceeded, 504},
		{fmt.Errorf("list: %w", context.DeadlineExceeded), apperr.CatDeadlineExceeded, codes.DeadlineExceeded, 504},
		{context.Canceled, apperr.CatCanceled, codes.Canceled, 499},
		{fmt.Errorf("list: %w", context.Canceled), apperr.CatCanceled, codes.Canceled, 499},
	}
	for _, tc := range cases {
		t.Run(tc.err.Error(), func(t *testing.T) {
			m, _ := apperr.Lookup(tc.cat)
			if st := apperr.ToGRPCStatus(tc.err); st.Code() != tc.code || st.Message() != m.SafeMessage {
				t.Fatalf("gRPC = %v %q, want %v %q", st.Code(), st.Message(), tc.code, m.SafeMessage)
			}
			code, body := apperr.ToHTTP(tc.err)
			if code != tc.http || body.Code != string(tc.cat) || body.Message != m.SafeMessage {
				t.Fatalf("HTTP = %d %+v, want %d %s", code, body, tc.http, tc.cat)
			}
		})
	}
	if m := apperr.ToMCP(context.DeadlineExceeded); !m.IsRetryable {
		t.Fatalf("deadline exceeded should be retryable: %+v", m)
	}
}

// With must not mutate its receiver: calling it on a shared sentinel from
// concurrent requests would otherwise race on the fields map and leak one
// request's fields into another's log line.
func TestWithIsCopyOnWrite(t *testing.T) {
	sentinel := apperr.New(apperr.CatNotFound, "cache miss")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			e := sentinel.With("key", i)
			if e == sentinel || e.Fields()["key"] != i || len(e.Fields()) != 1 {
				t.Errorf("With(%d) = %p %v", i, e, e.Fields())
			}
		})
	}
	wg.Wait()
	if len(sentinel.Fields()) != 0 {
		t.Fatalf("sentinel fields mutated: %v", sentinel.Fields())
	}
	chained := sentinel.With("a", 1).With("b", 2)
	if len(chained.Fields()) != 2 {
		t.Fatalf("chained fields = %v", chained.Fields())
	}
}

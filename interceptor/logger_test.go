package interceptor_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc"

	"github.com/yshengliao/gortexa/apperr"
	"github.com/yshengliao/gortexa/interceptor"
)

func TestLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	ic := interceptor.Logger(logger)

	// Test success
	handler := func(ctx context.Context, req any) (any, error) {
		return nil, nil
	}

	_, err := ic(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"}, handler)
	if err != nil {
		t.Fatal(err)
	}

	output := buf.String()
	if !strings.Contains(output, `"method":"/test.Service/Method"`) {
		t.Errorf("missing method in log: %s", output)
	}
	if !strings.Contains(output, `"code":"OK"`) {
		t.Errorf("missing code in log: %s", output)
	}

	buf.Reset()

	// Test error
	appErr := apperr.New(apperr.CatNotFound, "item not found").With("item_id", "123")
	handlerErr := func(ctx context.Context, req any) (any, error) {
		return nil, appErr
	}

	_, err = ic(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/test.Service/MethodErr"}, handlerErr)
	if err == nil {
		t.Fatal("expected error")
	}

	output = buf.String()
	if !strings.Contains(output, `"method":"/test.Service/MethodErr"`) {
		t.Errorf("missing method in log: %s", output)
	}
	if !strings.Contains(output, `"code":"NotFound"`) {
		t.Errorf("missing code in log: %s", output)
	}
	if !strings.Contains(output, `"error.msg":"item not found"`) {
		t.Errorf("missing error msg in log: %s", output)
	}
	if !strings.Contains(output, `"item_id":"123"`) {
		t.Errorf("missing field in log: %s", output)
	}

	buf.Reset()

	// Test standard error
	handlerStdErr := func(ctx context.Context, req any) (any, error) {
		return nil, errors.New("standard error")
	}

	_, err = ic(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/test.Service/MethodStdErr"}, handlerStdErr)
	if err == nil {
		t.Fatal("expected error")
	}

	output = buf.String()
	if !strings.Contains(output, `"method":"/test.Service/MethodStdErr"`) {
		t.Errorf("missing method in log: %s", output)
	}
	if !strings.Contains(output, `"error":"standard error"`) {
		t.Errorf("missing error in log: %s", output)
	}
}

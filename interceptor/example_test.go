package interceptor_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	"github.com/yshengliao/gortexa/interceptor"
)

// A consumer whose services authenticate with a non-JWT scheme (here a fixed
// opaque bearer token) still runs the framework's full, fixed interceptor
// chain: it only supplies its own Authenticator. Everything else — recover,
// request-id, logging, load-shed, rate-limit, circuit-breaker, validation — is
// unchanged. A valid token reaches the handler; a wrong one is rejected at the
// auth stage, before validation.
func Example_staticBearerAuthenticator() {
	set, err := interceptor.NewSet(interceptor.Config{
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Authenticator: staticBearer{token: "s3cret-token"},
	})
	if err != nil {
		panic(err)
	}

	chain := set.ChainUnary() // the whole stock chain as one interceptor
	info := unaryInfo("/resource.v1.ResourceService/GetResource")
	req := &resourcev1.GetResourceRequest{Id: "abc"}
	handler := func(context.Context, any) (any, error) { return &resourcev1.Resource{Id: "abc"}, nil }

	_, err = chain(bearerCtx("s3cret-token"), req, info, handler)
	fmt.Println("valid token reaches handler:", err == nil)

	_, err = chain(bearerCtx("wrong"), req, info, handler)
	fmt.Println("wrong token reaches handler:", err == nil)

	// Output:
	// valid token reaches handler: true
	// wrong token reaches handler: false
}

package logic_test

import (
	"context"
	"testing"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/logic"
)

func TestGetResource(t *testing.T) {
	ctx := context.Background()
	svc := logic.NewResourceService()

	// Set up a resource
	created, err := svc.CreateResource(ctx, &resourcev1.CreateResourceRequest{
		Resource: &resourcev1.Resource{
			Name:  "Test Get",
			Owner: "tester",
		},
	})
	if err != nil {
		t.Fatalf("failed to setup resource: %v", err)
	}

	t.Run("Found", func(t *testing.T) {
		res, err := svc.GetResource(ctx, &resourcev1.GetResourceRequest{Id: created.GetId()})
		if err != nil {
			t.Fatalf("GetResource failed: %v", err)
		}
		if res.GetId() != created.GetId() {
			t.Errorf("expected id %q, got %q", created.GetId(), res.GetId())
		}
		if res.GetName() != "Test Get" {
			t.Errorf("expected name %q, got %q", "Test Get", res.GetName())
		}
	})

	t.Run("NotFound", func(t *testing.T) {
		res, err := svc.GetResource(ctx, &resourcev1.GetResourceRequest{Id: "unknown-id"})
		if err == nil {
			t.Fatalf("expected error, got resource: %v", res)
		}
		if !apperr.Is(err, apperr.CatNotFound) {
			t.Errorf("expected CatNotFound, got: %v", err)
		}
	})
}

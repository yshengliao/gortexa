package logic

import (
	"context"
	"testing"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	apperr "github.com/yshengliao/gortexa/internal/errors"
)

func TestDeleteResource(t *testing.T) {
	ctx := context.Background()
	svc := NewResourceService()

	// Setup: create a resource to delete
	res, err := svc.CreateResource(ctx, &resourcev1.CreateResourceRequest{
		Resource: &resourcev1.Resource{
			Name:  "Test Resource",
			Owner: "test-user",
		},
	})
	if err != nil {
		t.Fatalf("failed to create resource: %v", err)
	}

	id := res.GetId()

	// Test 1: Successfully delete the resource
	_, err = svc.DeleteResource(ctx, &resourcev1.DeleteResourceRequest{
		Id: id,
	})
	if err != nil {
		t.Errorf("DeleteResource returned unexpected error: %v", err)
	}

	// Verify the resource is actually gone by trying to get it
	_, err = svc.GetResource(ctx, &resourcev1.GetResourceRequest{
		Id: id,
	})
	if err == nil {
		t.Errorf("expected error when getting deleted resource, got nil")
	} else if e, ok := err.(*apperr.Error); ok && e.Category != apperr.CatNotFound {
		t.Errorf("expected NotFound error, got %v", e.Category)
	} else if !ok {
		t.Errorf("expected apperr.Error, got %T", err)
	}

	// Test 2: Deleting a non-existent resource
	_, err = svc.DeleteResource(ctx, &resourcev1.DeleteResourceRequest{
		Id: "non-existent-id",
	})
	if err == nil {
		t.Errorf("expected error when deleting non-existent resource, got nil")
	} else if e, ok := err.(*apperr.Error); ok && e.Category != apperr.CatNotFound {
		t.Errorf("expected NotFound error, got %v", e.Category)
	} else if !ok {
		t.Errorf("expected apperr.Error, got %T", err)
	}
}

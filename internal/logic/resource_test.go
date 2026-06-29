package logic_test

import (
	"context"
	"testing"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	"github.com/yshengliao/gortexa/internal/logic"
)

func TestResourceService(t *testing.T) {
	svc := logic.NewResourceService()
	ctx := context.Background()

	// List empty
	list, err := svc.ListResources(ctx, &resourcev1.ListResourcesRequest{})
	if err != nil {
		t.Fatalf("ListResources failed: %v", err)
	}
	if len(list.GetResources()) != 0 {
		t.Fatalf("expected 0 resources, got %d", len(list.GetResources()))
	}

	// Create
	created, err := svc.CreateResource(ctx, &resourcev1.CreateResourceRequest{
		Resource: &resourcev1.Resource{Name: "test-resource", Owner: "test-owner"},
	})
	if err != nil {
		t.Fatalf("CreateResource failed: %v", err)
	}
	if created.GetId() == "" {
		t.Fatalf("expected ID to be set")
	}
	if created.GetName() != "test-resource" || created.GetOwner() != "test-owner" {
		t.Fatalf("unexpected fields in created resource: %v", created)
	}
	id := created.GetId()

	// Get
	got, err := svc.GetResource(ctx, &resourcev1.GetResourceRequest{Id: id})
	if err != nil {
		t.Fatalf("GetResource failed: %v", err)
	}
	if got.GetName() != "test-resource" {
		t.Fatalf("unexpected Name: %q", got.GetName())
	}

	// Update
	updated, err := svc.UpdateResource(ctx, &resourcev1.UpdateResourceRequest{
		Resource: &resourcev1.Resource{Id: id, Name: "updated-resource", Owner: "test-owner", Status: resourcev1.Status_STATUS_ARCHIVED},
	})
	if err != nil {
		t.Fatalf("UpdateResource failed: %v", err)
	}
	if updated.GetName() != "updated-resource" || updated.GetStatus() != resourcev1.Status_STATUS_ARCHIVED {
		t.Fatalf("unexpected fields in updated resource: %v", updated)
	}

	// List by owner
	list, err = svc.ListResources(ctx, &resourcev1.ListResourcesRequest{Owner: "test-owner"})
	if err != nil {
		t.Fatalf("ListResources by owner failed: %v", err)
	}
	if len(list.GetResources()) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(list.GetResources()))
	}

	// Delete
	_, err = svc.DeleteResource(ctx, &resourcev1.DeleteResourceRequest{Id: id})
	if err != nil {
		t.Fatalf("DeleteResource failed: %v", err)
	}

	// Get after delete
	_, err = svc.GetResource(ctx, &resourcev1.GetResourceRequest{Id: id})
	if err == nil {
		t.Fatalf("expected error getting deleted resource")
	}
}

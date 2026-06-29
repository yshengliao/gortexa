package logic_test

import (
	"context"
	"testing"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	"github.com/yshengliao/gortexa/internal/logic"
)

func TestResourceService_ListResources(t *testing.T) {
	ctx := context.Background()
	svc := logic.NewResourceService()

	// Create some resources
	_, err := svc.CreateResource(ctx, &resourcev1.CreateResourceRequest{
		Resource: &resourcev1.Resource{
			Name:  "Res 1",
			Owner: "owner-1",
		},
	})
	if err != nil {
		t.Fatalf("Failed to create resource 1: %v", err)
	}

	_, err = svc.CreateResource(ctx, &resourcev1.CreateResourceRequest{
		Resource: &resourcev1.Resource{
			Name:  "Res 2",
			Owner: "owner-2",
		},
	})
	if err != nil {
		t.Fatalf("Failed to create resource 2: %v", err)
	}

	_, err = svc.CreateResource(ctx, &resourcev1.CreateResourceRequest{
		Resource: &resourcev1.Resource{
			Name:  "Res 3",
			Owner: "owner-1",
		},
	})
	if err != nil {
		t.Fatalf("Failed to create resource 3: %v", err)
	}

	t.Run("List all", func(t *testing.T) {
		resp, err := svc.ListResources(ctx, &resourcev1.ListResourcesRequest{})
		if err != nil {
			t.Fatalf("ListResources failed: %v", err)
		}
		if len(resp.Resources) != 3 {
			t.Errorf("Expected 3 resources, got %d", len(resp.Resources))
		}

		// check sorting (id is random, so we just check if it's sorted)
		for i := 1; i < len(resp.Resources); i++ {
			if resp.Resources[i-1].Id > resp.Resources[i].Id {
				t.Errorf("Resources are not sorted by ID: %v > %v", resp.Resources[i-1].Id, resp.Resources[i].Id)
			}
		}
	})

	t.Run("Filter by owner", func(t *testing.T) {
		resp, err := svc.ListResources(ctx, &resourcev1.ListResourcesRequest{
			Owner: "owner-1",
		})
		if err != nil {
			t.Fatalf("ListResources failed: %v", err)
		}
		if len(resp.Resources) != 2 {
			t.Errorf("Expected 2 resources for owner-1, got %d", len(resp.Resources))
		}
		for _, r := range resp.Resources {
			if r.Owner != "owner-1" {
				t.Errorf("Expected owner-1, got %s", r.Owner)
			}
		}

		// check sorting
		for i := 1; i < len(resp.Resources); i++ {
			if resp.Resources[i-1].Id > resp.Resources[i].Id {
				t.Errorf("Resources are not sorted by ID: %v > %v", resp.Resources[i-1].Id, resp.Resources[i].Id)
			}
		}
	})

	t.Run("Filter by non-existent owner", func(t *testing.T) {
		resp, err := svc.ListResources(ctx, &resourcev1.ListResourcesRequest{
			Owner: "owner-3",
		})
		if err != nil {
			t.Fatalf("ListResources failed: %v", err)
		}
		if len(resp.Resources) != 0 {
			t.Errorf("Expected 0 resources for owner-3, got %d", len(resp.Resources))
		}
	})
}

package logic

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/timestamppb"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	apperr "github.com/yshengliao/gortexa/internal/errors"
)

func TestResourceService(t *testing.T) {
	s := NewResourceService()
	now := time.Now()
	s.now = func() *timestamppb.Timestamp {
		return timestamppb.New(now)
	}

	ctx := context.Background()

	// 1. CreateResource
	t.Run("CreateResource", func(t *testing.T) {
		req := &resourcev1.CreateResourceRequest{
			Resource: &resourcev1.Resource{
				Name:  "Test Resource",
				Owner: "user1",
			},
		}
		res, err := s.CreateResource(ctx, req)
		if err != nil {
			t.Fatalf("CreateResource failed: %v", err)
		}
		if res.Id == "" {
			t.Errorf("CreateResource returned empty ID")
		}
		if res.Name != "Test Resource" {
			t.Errorf("CreateResource returned wrong Name: %v", res.Name)
		}
		if res.Owner != "user1" {
			t.Errorf("CreateResource returned wrong Owner: %v", res.Owner)
		}
		if res.Status != resourcev1.Status_STATUS_ACTIVE {
			t.Errorf("CreateResource returned wrong Status: %v", res.Status)
		}
		if !res.CreatedAt.AsTime().Equal(now) {
			t.Errorf("CreateResource returned wrong CreatedAt: %v", res.CreatedAt)
		}

		// missing resource error
		_, err = s.CreateResource(ctx, &resourcev1.CreateResourceRequest{})
		if err == nil {
			t.Errorf("CreateResource with nil resource should return error")
		}
		if err != nil {
			if e, ok := err.(*apperr.Error); ok {
				if e.Category != apperr.CatInvalidArgument {
					t.Errorf("Expected InvalidArgument, got %v", e.Category)
				}
			} else {
				t.Errorf("Expected *apperr.Error, got %T", err)
			}
		}
	})

	// Setup data for other tests
	req1 := &resourcev1.CreateResourceRequest{
		Resource: &resourcev1.Resource{
			Name:  "R1",
			Owner: "owner1",
		},
	}
	res1, _ := s.CreateResource(ctx, req1)

	req2 := &resourcev1.CreateResourceRequest{
		Resource: &resourcev1.Resource{
			Name:  "R2",
			Owner: "owner2",
		},
	}
	res2, _ := s.CreateResource(ctx, req2)

	// 2. GetResource
	t.Run("GetResource", func(t *testing.T) {
		got, err := s.GetResource(ctx, &resourcev1.GetResourceRequest{Id: res1.Id})
		if err != nil {
			t.Fatalf("GetResource failed: %v", err)
		}
		if diff := cmp.Diff(res1, got, protocmp.Transform()); diff != "" {
			t.Errorf("GetResource mismatch (-want +got):\n%s", diff)
		}

		// Not found error
		_, err = s.GetResource(ctx, &resourcev1.GetResourceRequest{Id: "nonexistent"})
		if err == nil {
			t.Errorf("GetResource with nonexistent ID should return error")
		}
		if err != nil {
			if e, ok := err.(*apperr.Error); ok {
				if e.Category != apperr.CatNotFound {
					t.Errorf("Expected NotFound, got %v", e.Category)
				}
			}
		}
	})

	// 3. ListResources
	t.Run("ListResources", func(t *testing.T) {
		// All
		res, err := s.ListResources(ctx, &resourcev1.ListResourcesRequest{})
		if err != nil {
			t.Fatalf("ListResources failed: %v", err)
		}
		if len(res.Resources) != 3 { // including the one from CreateResource test
			t.Errorf("ListResources returned wrong number of resources: got %d, want 3", len(res.Resources))
		}

		// By owner
		resOwner1, err := s.ListResources(ctx, &resourcev1.ListResourcesRequest{Owner: "owner1"})
		if err != nil {
			t.Fatalf("ListResources failed: %v", err)
		}
		if len(resOwner1.Resources) != 1 {
			t.Errorf("ListResources returned wrong number of resources for owner1: got %d, want 1", len(resOwner1.Resources))
		}
		if diff := cmp.Diff(res1, resOwner1.Resources[0], protocmp.Transform()); diff != "" {
			t.Errorf("ListResources mismatch (-want +got):\n%s", diff)
		}
	})

	// 4. UpdateResource
	t.Run("UpdateResource", func(t *testing.T) {
		req := &resourcev1.UpdateResourceRequest{
			Id:     res1.Id,
			Name:   proto.String("R1 Updated"),
			Owner:  proto.String("owner1"),
			Status: resourcev1.Status_STATUS_ARCHIVED.Enum(),
		}
		updated, err := s.UpdateResource(ctx, req)
		if err != nil {
			t.Fatalf("UpdateResource failed: %v", err)
		}
		if updated.Name != "R1 Updated" {
			t.Errorf("UpdateResource name mismatch: got %v", updated.Name)
		}
		if updated.Status != resourcev1.Status_STATUS_ARCHIVED {
			t.Errorf("UpdateResource status mismatch: got %v", updated.Status)
		}

		// Missing ID
		_, err = s.UpdateResource(ctx, &resourcev1.UpdateResourceRequest{})
		if err == nil {
			t.Errorf("UpdateResource with empty ID should return error")
		}

		// Not found
		_, err = s.UpdateResource(ctx, &resourcev1.UpdateResourceRequest{Id: "nonexistent"})
		if err == nil {
			t.Errorf("UpdateResource with nonexistent ID should return error")
		}
	})

	// 5. DeleteResource
	t.Run("DeleteResource", func(t *testing.T) {
		_, err := s.DeleteResource(ctx, &resourcev1.DeleteResourceRequest{Id: res2.Id})
		if err != nil {
			t.Fatalf("DeleteResource failed: %v", err)
		}

		// Verify deletion
		_, err = s.GetResource(ctx, &resourcev1.GetResourceRequest{Id: res2.Id})
		if err == nil {
			t.Errorf("GetResource should fail for deleted resource")
		}

		// Not found
		_, err = s.DeleteResource(ctx, &resourcev1.DeleteResourceRequest{Id: "nonexistent"})
		if err == nil {
			t.Errorf("DeleteResource with nonexistent ID should return error")
		}
	})
}

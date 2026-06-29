package logic_test

import (
	"context"
	"testing"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/logic"
)

func TestResourceService_UpdateResource(t *testing.T) {
	ctx := context.Background()
	svc := logic.NewResourceService()

	// 1. Setup: Create a resource to update later
	createReq := &resourcev1.CreateResourceRequest{
		Resource: &resourcev1.Resource{
			Name:   "initial-name",
			Owner:  "alice",
			Status: resourcev1.Status_STATUS_ACTIVE,
		},
	}
	createdRes, err := svc.CreateResource(ctx, createReq)
	if err != nil {
		t.Fatalf("failed to create resource: %v", err)
	}

	tests := []struct {
		name        string
		req         *resourcev1.UpdateResourceRequest
		wantErrCat  apperr.Category
		checkResult func(t *testing.T, res *resourcev1.Resource)
	}{
		{
			name:       "nil resource",
			req:        &resourcev1.UpdateResourceRequest{Resource: nil},
			wantErrCat: apperr.CatInvalidArgument,
		},
		{
			name:       "empty id",
			req:        &resourcev1.UpdateResourceRequest{Resource: &resourcev1.Resource{Id: ""}},
			wantErrCat: apperr.CatInvalidArgument,
		},
		{
			name:       "not found",
			req:        &resourcev1.UpdateResourceRequest{Resource: &resourcev1.Resource{Id: "nonexistent-id"}},
			wantErrCat: apperr.CatNotFound,
		},
		{
			name: "successful update",
			req: &resourcev1.UpdateResourceRequest{
				Resource: &resourcev1.Resource{
					Id:     createdRes.Id,
					Name:   "updated-name",
					Owner:  "bob",
					Status: resourcev1.Status_STATUS_ARCHIVED,
				},
			},
			checkResult: func(t *testing.T, res *resourcev1.Resource) {
				if res.Name != "updated-name" {
					t.Errorf("expected Name 'updated-name', got %q", res.Name)
				}
				if res.Owner != "bob" {
					t.Errorf("expected Owner 'bob', got %q", res.Owner)
				}
				if res.Status != resourcev1.Status_STATUS_ARCHIVED {
					t.Errorf("expected Status INACTIVE, got %v", res.Status)
				}
			},
		},
		{
			name: "unspecified status doesn't overwrite",
			req: &resourcev1.UpdateResourceRequest{
				Resource: &resourcev1.Resource{
					Id:     createdRes.Id,
					Name:   "new-name",
					Owner:  "charlie",
					Status: resourcev1.Status_STATUS_UNSPECIFIED,
				},
			},
			checkResult: func(t *testing.T, res *resourcev1.Resource) {
				// From previous update it was INACTIVE
				if res.Status != resourcev1.Status_STATUS_ARCHIVED {
					t.Errorf("expected Status to remain INACTIVE, got %v", res.Status)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := svc.UpdateResource(ctx, tt.req)
			if tt.wantErrCat != "" {
				if err == nil {
					t.Fatalf("expected error category %q, got nil", tt.wantErrCat)
				}
				e, ok := err.(*apperr.Error)
				if !ok {
					t.Fatalf("expected *apperr.Error, got %T", err)
				}
				if e.Category != tt.wantErrCat {
					t.Errorf("expected error category %q, got %q", tt.wantErrCat, e.Category)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.checkResult != nil {
				tt.checkResult(t, res)
			}
		})
	}
}

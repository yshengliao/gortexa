package logic

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/timestamppb"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	apperr "github.com/yshengliao/gortexa/internal/errors"
)

func TestCreateResource(t *testing.T) {
	s := NewResourceService()

	// Fix the time for testing
	now := timestamppb.Now()
	s.now = func() *timestamppb.Timestamp {
		return now
	}

	tests := []struct {
		name    string
		req     *resourcev1.CreateResourceRequest
		want    *resourcev1.Resource
		wantErr error
	}{
		{
			name: "success with explicit status",
			req: &resourcev1.CreateResourceRequest{
				Resource: &resourcev1.Resource{
					Name:   "test-resource",
					Owner:  "user-1",
					Status: resourcev1.Status_STATUS_ACTIVE,
				},
			},
			want: &resourcev1.Resource{
				Name:      "test-resource",
				Owner:     "user-1",
				Status:    resourcev1.Status_STATUS_ACTIVE,
				CreatedAt: now,
			},
		},
		{
			name: "success defaults status unspecified to active",
			req: &resourcev1.CreateResourceRequest{
				Resource: &resourcev1.Resource{
					Name:  "test-resource-2",
					Owner: "user-2",
				},
			},
			want: &resourcev1.Resource{
				Name:      "test-resource-2",
				Owner:     "user-2",
				Status:    resourcev1.Status_STATUS_ACTIVE,
				CreatedAt: now,
			},
		},
		{
			name:    "error nil resource",
			req:     &resourcev1.CreateResourceRequest{},
			wantErr: apperr.New(apperr.CatInvalidArgument, "resource is required"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.CreateResource(context.Background(), tt.req)

			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tt.wantErr)
				}
				if err.Error() != tt.wantErr.Error() {
					t.Errorf("expected error %v, got %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Check that ID is assigned
			if got.GetId() == "" {
				t.Errorf("expected ID to be assigned, got empty string")
			}

			// We need to ignore the ID for comparison because it's random
			opts := []cmp.Option{
				protocmp.Transform(),
				protocmp.IgnoreFields(&resourcev1.Resource{}, "id"),
			}

			if diff := cmp.Diff(tt.want, got, opts...); diff != "" {
				t.Errorf("CreateResource() mismatch (-want +got):\n%s", diff)
			}

			// Verify it was stored
			s.mu.RLock()
			stored, ok := s.store[got.GetId()]
			s.mu.RUnlock()
			if !ok {
				t.Errorf("expected resource to be stored in map, but wasn't")
			}

			if diff := cmp.Diff(got, stored, protocmp.Transform()); diff != "" {
				t.Errorf("stored resource differs from returned (-returned +stored):\n%s", diff)
			}
		})
	}
}

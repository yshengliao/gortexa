// Package logic holds Gortexa's sample business logic: an in-memory
// ResourceService used to exercise the framework end-to-end (gRPC, gateway,
// MCP). Real services would back this with internal/storage.
package logic

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	apperr "github.com/yshengliao/gortexa/apperr"
	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
)

// clone returns a deep copy so callers (and concurrent readers) never share a
// pointer with the live store. This is the store's mutation-isolation boundary
// and is intentionally kept despite the per-call copy cost (see
// BenchmarkResourceClone); removing it would let a caller mutate stored state.
func clone(r *resourcev1.Resource) *resourcev1.Resource {
	return proto.Clone(r).(*resourcev1.Resource)
}

// ResourceService is an in-memory implementation of resource.v1.ResourceService.
type ResourceService struct {
	resourcev1.UnimplementedResourceServiceServer
	mu    sync.RWMutex
	store map[string]*resourcev1.Resource
	now   func() *timestamppb.Timestamp
}

// NewResourceService returns an empty in-memory service.
func NewResourceService() *ResourceService {
	return &ResourceService{
		store: make(map[string]*resourcev1.Resource),
		now:   timestamppb.Now,
	}
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// defaultPageSize bounds a ListResources page when the caller does not set one.
const defaultPageSize = 50

// CreateResource stores a new resource, assigning an id and timestamp.
func (s *ResourceService) CreateResource(_ context.Context, req *resourcev1.CreateResourceRequest) (*resourcev1.Resource, error) {
	if req.GetResource() == nil {
		return nil, apperr.New(apperr.CatInvalidArgument, "resource is required")
	}
	r := req.GetResource()
	out := &resourcev1.Resource{
		Id:        newID(),
		Name:      r.GetName(),
		Owner:     r.GetOwner(),
		Status:    r.GetStatus(),
		CreatedAt: s.now(),
	}
	if out.Status == resourcev1.Status_STATUS_UNSPECIFIED {
		out.Status = resourcev1.Status_STATUS_ACTIVE
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store[out.Id] = out
	// Clone under the lock: a concurrent UpdateResource mutates the stored
	// message in place, so cloning after releasing the lock would race.
	return clone(out), nil
}

// GetResource returns a resource by id.
func (s *ResourceService) GetResource(_ context.Context, req *resourcev1.GetResourceRequest) (*resourcev1.Resource, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.store[req.GetId()]
	if !ok {
		return nil, apperr.New(apperr.CatNotFound, "resource not found")
	}
	// Clone under the read lock; UpdateResource mutates the stored message in
	// place under the write lock, so an unlocked clone would be a data race.
	return clone(r), nil
}

// ListResources returns resources, optionally filtered by owner, with paging.
// Results are ordered by id; page_token is the id of the last item on the
// previous page (results resume strictly after it).
func (s *ResourceService) ListResources(_ context.Context, req *resourcev1.ListResourcesRequest) (*resourcev1.ListResourcesResponse, error) {
	s.mu.RLock()
	all := make([]*resourcev1.Resource, 0, len(s.store))
	for _, r := range s.store {
		if req.GetOwner() == "" || r.GetOwner() == req.GetOwner() {
			all = append(all, clone(r))
		}
	}
	s.mu.RUnlock()
	slices.SortFunc(all, func(a, b *resourcev1.Resource) int { return cmp.Compare(a.GetId(), b.GetId()) })

	// Seek past the page token (the last id returned on the previous page).
	start := 0
	if tok := req.GetPageToken(); tok != "" {
		start = len(all)
		for i, r := range all {
			if r.GetId() > tok {
				start = i
				break
			}
		}
	}
	size := int(req.GetPageSize())
	if size <= 0 {
		size = defaultPageSize
	}
	end := min(start+size, len(all))
	page := all[start:end]

	resp := &resourcev1.ListResourcesResponse{Resources: page}
	if end < len(all) && len(page) > 0 {
		resp.NextPageToken = page[len(page)-1].GetId()
	}
	return resp, nil
}

// UpdateResource applies a partial update (PATCH): only fields present in the
// request replace the stored values, so omitting a field leaves it untouched.
func (s *ResourceService) UpdateResource(_ context.Context, req *resourcev1.UpdateResourceRequest) (*resourcev1.Resource, error) {
	if req.GetId() == "" {
		return nil, apperr.New(apperr.CatInvalidArgument, "resource.id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.store[req.GetId()]
	if !ok {
		return nil, apperr.New(apperr.CatNotFound, "resource not found")
	}
	if req.Name != nil {
		existing.Name = req.GetName()
	}
	if req.Owner != nil {
		existing.Owner = req.GetOwner()
	}
	if req.Status != nil {
		existing.Status = req.GetStatus()
	}
	return clone(existing), nil
}

// DeleteResource removes a resource by id.
func (s *ResourceService) DeleteResource(_ context.Context, req *resourcev1.DeleteResourceRequest) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.store[req.GetId()]; !ok {
		return nil, apperr.New(apperr.CatNotFound, "resource not found")
	}
	delete(s.store, req.GetId())
	return &emptypb.Empty{}, nil
}

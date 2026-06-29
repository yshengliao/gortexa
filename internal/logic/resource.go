// Package logic holds Gortexa's sample business logic: an in-memory
// ResourceService used to exercise the framework end-to-end (gRPC, gateway,
// MCP). Real services would back this with internal/storage.
package logic

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	apperr "github.com/yshengliao/gortexa/internal/errors"
)

// clone returns a deep copy so callers (and concurrent readers) never share a
// pointer with the live store.
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
	s.store[out.Id] = out
	s.mu.Unlock()
	return clone(out), nil
}

// GetResource returns a resource by id.
func (s *ResourceService) GetResource(_ context.Context, req *resourcev1.GetResourceRequest) (*resourcev1.Resource, error) {
	s.mu.RLock()
	r, ok := s.store[req.GetId()]
	s.mu.RUnlock()
	if !ok {
		return nil, apperr.New(apperr.CatNotFound, "resource not found")
	}
	return clone(r), nil
}

// ListResources returns resources, optionally filtered by owner.
func (s *ResourceService) ListResources(_ context.Context, req *resourcev1.ListResourcesRequest) (*resourcev1.ListResourcesResponse, error) {
	s.mu.RLock()
	out := make([]*resourcev1.Resource, 0, len(s.store))
	for _, r := range s.store {
		if req.GetOwner() == "" || r.GetOwner() == req.GetOwner() {
			out = append(out, clone(r))
		}
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].GetId() < out[j].GetId() })
	return &resourcev1.ListResourcesResponse{Resources: out}, nil
}

// UpdateResource replaces a resource's mutable fields.
func (s *ResourceService) UpdateResource(_ context.Context, req *resourcev1.UpdateResourceRequest) (*resourcev1.Resource, error) {
	r := req.GetResource()
	if r == nil || r.GetId() == "" {
		return nil, apperr.New(apperr.CatInvalidArgument, "resource.id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.store[r.GetId()]
	if !ok {
		return nil, apperr.New(apperr.CatNotFound, "resource not found")
	}
	existing.Name = r.GetName()
	existing.Owner = r.GetOwner()
	if r.GetStatus() != resourcev1.Status_STATUS_UNSPECIFIED {
		existing.Status = r.GetStatus()
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

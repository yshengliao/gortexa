package logic

import (
	"context"
	"fmt"
	"sync"
	"testing"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
)

// TestUpdateResourcePartial verifies PATCH semantics: a field omitted from the
// request keeps its stored value instead of being cleared.
func TestUpdateResourcePartial(t *testing.T) {
	s := NewResourceService()
	ctx := context.Background()
	created, err := s.CreateResource(ctx, &resourcev1.CreateResourceRequest{
		Resource: &resourcev1.Resource{Name: "orig", Owner: "owner-a", Status: resourcev1.Status_STATUS_ACTIVE},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Update only Name; Owner and Status must be preserved.
	got, err := s.UpdateResource(ctx, &resourcev1.UpdateResourceRequest{
		Id: created.Id, Name: new("renamed"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Name != "renamed" {
		t.Errorf("Name = %q, want renamed", got.Name)
	}
	if got.Owner != "owner-a" {
		t.Errorf("Owner = %q, want owner-a preserved (PATCH must not clear omitted fields)", got.Owner)
	}
	if got.Status != resourcev1.Status_STATUS_ACTIVE {
		t.Errorf("Status = %v, want STATUS_ACTIVE preserved", got.Status)
	}
}

// TestListResourcesPaging verifies page_size limits the page and page_token
// resumes strictly after the previous page's last id, with next_page_token set
// only while more remain.
func TestListResourcesPaging(t *testing.T) {
	s := NewResourceService()
	ctx := context.Background()
	const total = 5
	for i := range total {
		if _, err := s.CreateResource(ctx, &resourcev1.CreateResourceRequest{
			Resource: &resourcev1.Resource{Name: fmt.Sprintf("r%d", i), Owner: "o"},
		}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	seen := map[string]bool{}
	token := ""
	pages := 0
	for {
		resp, err := s.ListResources(ctx, &resourcev1.ListResourcesRequest{PageSize: 2, PageToken: token})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(resp.Resources) > 2 {
			t.Fatalf("page size exceeded: got %d", len(resp.Resources))
		}
		for _, r := range resp.Resources {
			if seen[r.Id] {
				t.Fatalf("duplicate id %q across pages", r.Id)
			}
			seen[r.Id] = true
		}
		pages++
		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
		if pages > total+2 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(seen) != total {
		t.Errorf("saw %d unique resources across pages, want %d", len(seen), total)
	}
}

// TestConcurrentGetUpdateNoRace exercises the clone-under-lock fix: concurrent
// Get/Create against Update on the same id must not trip the race detector.
func TestConcurrentGetUpdateNoRace(t *testing.T) {
	s := NewResourceService()
	ctx := context.Background()
	created, err := s.CreateResource(ctx, &resourcev1.CreateResourceRequest{
		Resource: &resourcev1.Resource{Name: "n", Owner: "o"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = s.GetResource(ctx, &resourcev1.GetResourceRequest{Id: created.Id}) }()
		go func(n int) {
			defer wg.Done()
			_, _ = s.UpdateResource(ctx, &resourcev1.UpdateResourceRequest{
				Id: created.Id, Name: new(fmt.Sprintf("n%d", n)), Owner: new(fmt.Sprintf("o%d", n)),
			})
		}(i)
	}
	wg.Wait()
}

package logic_test

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	"github.com/yshengliao/gortexa/internal/logic"
)

// proto.Clone is the store's mutation-isolation boundary; this quantifies its cost.
func BenchmarkResourceClone(b *testing.B) {
	r := &resourcev1.Resource{
		Id: "abcdef0123456789", Name: "alpha", Owner: "u-1",
		Status: resourcev1.Status_STATUS_ACTIVE, CreatedAt: timestamppb.Now(),
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = proto.Clone(r)
	}
}

// End-to-end read: RLock + map lookup + clone.
func BenchmarkGetResource(b *testing.B) {
	svc := logic.NewResourceService()
	created, err := svc.CreateResource(context.Background(), &resourcev1.CreateResourceRequest{
		Resource: &resourcev1.Resource{Name: "alpha", Owner: "u-1"},
	})
	if err != nil {
		b.Fatal(err)
	}
	req := &resourcev1.GetResourceRequest{Id: created.GetId()}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.GetResource(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}

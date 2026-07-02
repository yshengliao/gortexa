package kernel_test

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yshengliao/gortexa/internal/kernel"
)

type Greeter interface{ Greet() string }

type englishGreeter struct{}

func (englishGreeter) Greet() string { return "hello" }

func TestRegisterValueAndGet(t *testing.T) {
	c := kernel.NewContainer()
	kernel.RegisterValue(c, 42)
	got, err := kernel.Get[int](c)
	if err != nil || got != 42 {
		t.Fatalf("Get[int] = %d, %v", got, err)
	}
}

func TestLazySingleton(t *testing.T) {
	c := kernel.NewContainer()
	var built atomic.Int32
	kernel.Register(c, func() string {
		built.Add(1)
		return "x"
	})
	for range 3 {
		if _, err := kernel.Get[string](c); err != nil {
			t.Fatal(err)
		}
	}
	if built.Load() != 1 {
		t.Fatalf("provider built %d times, want 1", built.Load())
	}
}

func TestInterfaceRegistration(t *testing.T) {
	c := kernel.NewContainer()
	kernel.Register[Greeter](c, func() Greeter { return englishGreeter{} })
	g, err := kernel.Get[Greeter](c)
	if err != nil {
		t.Fatal(err)
	}
	if g.Greet() != "hello" {
		t.Fatalf("Greet = %q", g.Greet())
	}
}

func TestMissingProvider(t *testing.T) {
	c := kernel.NewContainer()
	if _, err := kernel.Get[float64](c); err == nil {
		t.Fatal("expected error for missing provider")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("MustGet should panic on missing provider")
		}
	}()
	kernel.MustGet[float64](c)
}

// A provider that panics must be recovered and surfaced as an error. Because
// sync.Once marks the entry done even when build panics, a naive implementation
// would leave the value nil and a *second* Get would report a misleading
// "produced <nil>" type mismatch. Both resolves must return the same
// panic-derived error instead.
func TestGetPanickingProviderReturnsError(t *testing.T) {
	c := kernel.NewContainer()
	kernel.Register(c, func() string {
		panic("boom")
	})

	_, err1 := kernel.Get[string](c)
	if err1 == nil {
		t.Fatal("first Get on a panicking provider: want error, got nil")
	}
	if !strings.Contains(err1.Error(), "panicked") {
		t.Fatalf("first Get error = %q, want it to mention \"panicked\"", err1)
	}

	// The entry is already done; the second resolve must surface the stored
	// panic error, not a nil-value type mismatch ("produced <nil>").
	_, err2 := kernel.Get[string](c)
	if err2 == nil {
		t.Fatal("second Get on a panicking provider: want error, got nil")
	}
	if err1.Error() != err2.Error() {
		t.Fatalf("second Get error = %q, want same as first %q", err2, err1)
	}
	if strings.Contains(err2.Error(), "produced") {
		t.Fatalf("second Get leaked a type-mismatch error: %q", err2)
	}
}

// MustGet returns the resolved value on the happy path (fail-loud only on error).
func TestMustGetSuccess(t *testing.T) {
	c := kernel.NewContainer()
	kernel.RegisterValue(c, "ok")
	if got := kernel.MustGet[string](c); got != "ok" {
		t.Fatalf("MustGet = %q, want %q", got, "ok")
	}
}

// MustGet must panic when its provider panicked (the recovered error propagates).
func TestMustGetPanickingProviderPanics(t *testing.T) {
	c := kernel.NewContainer()
	kernel.Register(c, func() int { panic("nope") })
	defer func() {
		if recover() == nil {
			t.Fatal("MustGet should panic when the provider panicked")
		}
	}()
	kernel.MustGet[int](c)
}

func TestConcurrentResolveBuildsOnce(t *testing.T) {
	c := kernel.NewContainer()
	var built atomic.Int32
	kernel.Register(c, func() *int {
		built.Add(1)
		n := 7
		return &n
	})
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if _, err := kernel.Get[*int](c); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if built.Load() != 1 {
		t.Fatalf("built %d times under concurrency, want 1", built.Load())
	}
}

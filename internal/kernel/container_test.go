package kernel_test

import (
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

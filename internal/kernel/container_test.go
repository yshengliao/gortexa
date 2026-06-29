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

type selfCycle struct{}

type indirectCycleA struct{}

type indirectCycleB struct{}

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
	for i := 0; i < 3; i++ {
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
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := kernel.Get[*int](c); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if built.Load() != 1 {
		t.Fatalf("built %d times under concurrency, want 1", built.Load())
	}
}

func TestDirectSelfCycle(t *testing.T) {
	c := kernel.NewContainer()
	var cycleErr error
	kernel.Register(c, func() *selfCycle {
		_, cycleErr = kernel.Get[*selfCycle](c)
		return &selfCycle{}
	})

	if _, err := kernel.Get[*selfCycle](c); err != nil {
		t.Fatalf("Get[*selfCycle] unexpected outer error: %v", err)
	}
	if cycleErr == nil {
		t.Fatal("expected dependency cycle error")
	}
	if got, want := cycleErr.Error(), "kernel: dependency cycle: *kernel_test.selfCycle -> *kernel_test.selfCycle"; got != want {
		t.Fatalf("cycle error = %q, want %q", got, want)
	}
}

func TestIndirectCycle(t *testing.T) {
	c := kernel.NewContainer()
	var cycleErr error
	kernel.Register(c, func() *indirectCycleA {
		if _, err := kernel.Get[*indirectCycleB](c); err != nil {
			t.Fatalf("Get[*indirectCycleB] unexpected error: %v", err)
		}
		return &indirectCycleA{}
	})
	kernel.Register(c, func() *indirectCycleB {
		_, cycleErr = kernel.Get[*indirectCycleA](c)
		return &indirectCycleB{}
	})

	if _, err := kernel.Get[*indirectCycleA](c); err != nil {
		t.Fatalf("Get[*indirectCycleA] unexpected outer error: %v", err)
	}
	if cycleErr == nil {
		t.Fatal("expected dependency cycle error")
	}
	got := cycleErr.Error()
	for _, want := range []string{
		"kernel: dependency cycle: ",
		"*kernel_test.indirectCycleA",
		"*kernel_test.indirectCycleB",
		" -> ",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("cycle error = %q, want substring %q", got, want)
		}
	}
	if want := "*kernel_test.indirectCycleA -> *kernel_test.indirectCycleB -> *kernel_test.indirectCycleA"; !strings.Contains(got, want) {
		t.Fatalf("cycle error = %q, want path %q", got, want)
	}
}

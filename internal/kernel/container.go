// Package kernel holds Gortexa's composition root: a generic dependency
// container, the App lifecycle, and the single-port h2c multiplexer that
// dispatches one listener across gRPC, the HTTP/JSON gateway, and MCP.
package kernel

import (
	"fmt"
	"reflect"
	"sync"
)

// Container is a minimal generic DI container. Providers are lazy singletons:
// each type is constructed at most once, on first resolution. It replaces
// gortex's never-implemented struct-tag injection with real generics.
type Container struct {
	mu      sync.RWMutex
	entries map[reflect.Type]*entry
}

type entry struct {
	once  sync.Once
	build func() any
	val   any
}

// NewContainer returns an empty container.
func NewContainer() *Container {
	return &Container{entries: make(map[reflect.Type]*entry)}
}

func typeKey[T any]() reflect.Type {
	// Works for interface and concrete T alike.
	return reflect.TypeOf((*T)(nil)).Elem()
}

func (c *Container) set(t reflect.Type, build func() any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[t] = &entry{build: build}
}

func (c *Container) resolve(t reflect.Type) (any, error) {
	c.mu.RLock()
	e, ok := c.entries[t]
	c.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("kernel: no provider registered for %s", t)
	}
	// once.Do permits a builder to resolve *other* types (different once);
	// only a same-type cycle would deadlock, which is a real programming error.
	e.once.Do(func() { e.val = e.build() })
	return e.val, nil
}

// Register registers a lazy singleton provider for T.
func Register[T any](c *Container, f func() T) {
	c.set(typeKey[T](), func() any { return f() })
}

// RegisterValue registers an already-constructed value for T.
func RegisterValue[T any](c *Container, v T) {
	Register(c, func() T { return v })
}

// Get resolves T, constructing it on first use.
func Get[T any](c *Container) (T, error) {
	var zero T
	v, err := c.resolve(typeKey[T]())
	if err != nil {
		return zero, err
	}
	t, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("kernel: provider for %s produced %T", typeKey[T](), v)
	}
	return t, nil
}

// MustGet resolves T or panics (fail-loud at startup).
func MustGet[T any](c *Container) T {
	v, err := Get[T](c)
	if err != nil {
		panic(err)
	}
	return v
}

// Package kernel holds Gortexa's composition root: a generic dependency
// container, the App lifecycle, and the single-port h2c multiplexer that
// dispatches one listener across gRPC, the HTTP/JSON gateway, and MCP.
package kernel

import (
	"bytes"
	"fmt"
	"reflect"
	"runtime"
	"strconv"
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
	mu        sync.Mutex
	cond      *sync.Cond
	build     func() any
	val       any
	built     bool
	resolving bool
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
	e := &entry{build: build}
	e.cond = sync.NewCond(&e.mu)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[t] = e
}

func (c *Container) resolve(t reflect.Type) (any, error) {
	return c.resolveWithStack(t, currentGoroutineID())
}

func (c *Container) resolveWithStack(t reflect.Type, gid uint64) (any, error) {
	c.mu.RLock()
	e, ok := c.entries[t]
	c.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("kernel: no provider registered for %s", t)
	}

	stack := resolutionStack(gid)
	e.mu.Lock()
	for {
		if e.built {
			v := e.val
			e.mu.Unlock()
			return v, nil
		}
		if !e.resolving {
			break
		}
		if cycle, ok := dependencyCycle(stack, t); ok {
			e.mu.Unlock()
			return nil, fmt.Errorf("kernel: dependency cycle: %s", formatTypePath(cycle))
		}
		e.cond.Wait()
	}
	e.resolving = true
	e.mu.Unlock()

	pushResolution(gid, t)
	defer func() {
		if r := recover(); r != nil {
			e.mu.Lock()
			e.resolving = false
			e.mu.Unlock()
			e.cond.Broadcast()
			panic(r)
		}
		popResolution(gid)
	}()

	v := e.build()
	e.mu.Lock()
	e.val = v
	e.built = true
	e.resolving = false
	e.mu.Unlock()
	e.cond.Broadcast()
	return v, nil
}

var (
	resolutionStacksMu sync.Mutex
	resolutionStacks   = make(map[uint64][]reflect.Type)
)

func currentGoroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	fields := bytes.Fields(buf[:n])
	if len(fields) < 2 {
		return 0
	}
	id, err := strconv.ParseUint(string(fields[1]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func resolutionStack(gid uint64) []reflect.Type {
	resolutionStacksMu.Lock()
	defer resolutionStacksMu.Unlock()
	stack := resolutionStacks[gid]
	return append([]reflect.Type(nil), stack...)
}

func pushResolution(gid uint64, t reflect.Type) {
	resolutionStacksMu.Lock()
	defer resolutionStacksMu.Unlock()
	resolutionStacks[gid] = append(resolutionStacks[gid], t)
}

func popResolution(gid uint64) {
	resolutionStacksMu.Lock()
	defer resolutionStacksMu.Unlock()
	stack := resolutionStacks[gid]
	if len(stack) <= 1 {
		delete(resolutionStacks, gid)
		return
	}
	resolutionStacks[gid] = stack[:len(stack)-1]
}

func dependencyCycle(stack []reflect.Type, t reflect.Type) ([]reflect.Type, bool) {
	for i, typ := range stack {
		if typ == t {
			cycle := append([]reflect.Type(nil), stack[i:]...)
			cycle = append(cycle, t)
			return cycle, true
		}
	}
	return nil, false
}

func formatTypePath(path []reflect.Type) string {
	if len(path) == 0 {
		return ""
	}
	out := path[0].String()
	for _, t := range path[1:] {
		out += " -> " + t.String()
	}
	return out
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

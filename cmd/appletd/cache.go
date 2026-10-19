package main

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"sync"

	"github.com/skybitsnl/go-applets/build"
	"github.com/skybitsnl/go-applets/runner"
)

// buildCache keeps the results of building applets: a module, or the
// InvalidSourceError of source that does not build. Builds are
// deterministic, so a result holds for every stream that sends the same
// source, declarations and signature. Within one process, the versions of
// tsc and Javy cannot change, so they need not be part of the key.
type buildCache struct {
	builder *build.Builder
	max     int

	mu sync.Mutex
	// order has the entries, the most recently used first.
	order   *list.List
	entries map[[sha256.Size]byte]*list.Element
}

type buildEntry struct {
	key  [sha256.Size]byte
	wasm []byte
	err  error
}

func newBuildCache(builder *build.Builder, max int) *buildCache {
	return &buildCache{builder: builder, max: max, order: list.New(), entries: map[[sha256.Size]byte]*list.Element{}}
}

// build builds an applet, or returns the result of building it before.
func (c *buildCache) build(ctx context.Context, source string, env build.Env) ([]byte, error) {
	key := buildKey(source, env)
	c.mu.Lock()
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		entry := el.Value.(*buildEntry)
		c.mu.Unlock()
		return entry.wasm, entry.err
	}
	c.mu.Unlock()

	// Concurrent streams may build the same applet at the same time. That
	// is wasteful, but gives the same result.
	wasm, err := c.builder.Build(ctx, source, env)
	var invalid *build.InvalidSourceError
	if err != nil && !errors.As(err, &invalid) {
		// Not a property of the source, e.g. a cancelled context.
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; !ok && c.max > 0 {
		c.entries[key] = c.order.PushFront(&buildEntry{key: key, wasm: wasm, err: err})
		for c.order.Len() > c.max {
			delete(c.entries, c.order.Remove(c.order.Back()).(*buildEntry).key)
		}
	}
	return wasm, err
}

// buildKey hashes the inputs of a build. Each is prefixed with its length,
// so that no two different inputs hash the same bytes.
func buildKey(source string, env build.Env) [sha256.Size]byte {
	h := sha256.New()
	for _, s := range []string{source, env.Declarations, env.Signature} {
		h.Write(binary.LittleEndian.AppendUint64(nil, uint64(len(s))))
		io.WriteString(h, s)
	}
	return [sha256.Size]byte(h.Sum(nil))
}

// engineKey is what an engine is created with, besides the dependencies.
type engineKey struct {
	memoryMiB          uint32
	closeOnContextDone bool
}

// engineCache keeps an engine for each engineKey in use, and up to max idle
// ones. Creating an engine compiles the dependencies, which takes long.
type engineCache struct {
	deps map[string][]byte
	max  int

	mu sync.Mutex
	// clock orders the uses of engines, to find the least recently used.
	clock   uint64
	entries map[engineKey]*engineEntry
}

type engineEntry struct {
	// ready is closed when engine or err is set.
	ready  chan struct{}
	engine *runner.Engine
	err    error

	// The number of streams that acquired the engine and did not release it
	// yet. An engine is only closed when this is 0.
	refs     int
	lastUsed uint64
}

func newEngineCache(deps map[string][]byte, max int) *engineCache {
	return &engineCache{deps: deps, max: max, entries: map[engineKey]*engineEntry{}}
}

// acquire returns the engine for key, creating it if needed. The engine stays
// open until the returned function is called.
func (c *engineCache) acquire(ctx context.Context, key engineKey) (*runner.Engine, func(), error) {
	c.mu.Lock()
	entry := c.entries[key]
	if entry == nil {
		entry = &engineEntry{ready: make(chan struct{})}
		c.entries[key] = entry
		// Create the engine in a goroutine, then wait for the engine to be
		// created in this function; this allows multiple streams to wait for
		// the same engine
		go c.create(key, entry)
	}
	entry.refs++
	c.clock++
	entry.lastUsed = c.clock
	c.mu.Unlock()

	var once sync.Once
	release := func() { once.Do(func() { c.release(entry) }) }
	select {
	case <-entry.ready:
	case <-ctx.Done():
		release()
		return nil, nil, ctx.Err()
	}
	if entry.err != nil {
		release()
		return nil, nil, entry.err
	}
	return entry.engine, release, nil
}

func (c *engineCache) create(key engineKey, entry *engineEntry) {
	limits := runner.Limits{}.WithMemoryMiB(key.memoryMiB).WithCloseOnContextDone(key.closeOnContextDone)
	entry.engine, entry.err = runner.NewEngine(context.Background(), c.deps, limits)
	close(entry.ready)

	c.mu.Lock()
	if entry.err != nil && c.entries[key] == entry {
		// Try again for the next stream.
		delete(c.entries, key)
	}
	idle := c.evict()
	c.mu.Unlock()
	closeEngines(idle)
}

func (c *engineCache) release(entry *engineEntry) {
	c.mu.Lock()
	entry.refs--
	// It was in use until now.
	c.clock++
	entry.lastUsed = c.clock
	idle := c.evict()
	c.mu.Unlock()
	closeEngines(idle)
}

// evict removes the least recently used idle engines while there are more
// than max, and returns them to be closed. Engines in use do not count
// towards max. c.mu must be held.
func (c *engineCache) evict() []*runner.Engine {
	var evicted []*runner.Engine
	for {
		var (
			idle     int
			oldest   *engineEntry
			oldestAt engineKey
		)
		for key, entry := range c.entries {
			if entry.refs > 0 || !isClosed(entry.ready) {
				continue
			}
			idle++
			if oldest == nil || entry.lastUsed < oldest.lastUsed {
				oldest, oldestAt = entry, key
			}
		}
		if idle <= c.max {
			return evicted
		}
		delete(c.entries, oldestAt)
		evicted = append(evicted, oldest.engine)
	}
}

// close closes all engines. Streams must have ended.
func (c *engineCache) close() {
	c.mu.Lock()
	entries := c.entries
	c.entries = map[engineKey]*engineEntry{}
	c.mu.Unlock()
	for _, entry := range entries {
		<-entry.ready
		if entry.engine != nil {
			entry.engine.Close(context.Background())
		}
	}
}

func closeEngines(engines []*runner.Engine) {
	for _, e := range engines {
		e.Close(context.Background())
	}
}

func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

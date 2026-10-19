package main

import (
	"context"
	"errors"
	"testing"

	"github.com/skybitsnl/go-applets/build"
)

func TestBuildCache(t *testing.T) {
	ctx := context.Background()
	c := newBuildCache(builder, 1)
	env := build.Env{Signature: anySignature}
	other := `export default function* (input: Iterable<object>) { for (const obj of input) yield obj; }`

	// A cached module is the same slice.
	first, err := c.build(ctx, passThrough, env)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := c.build(ctx, passThrough, env); &again[0] != &first[0] {
		t.Error("the second build was not cached")
	}
	if withDecls, _ := c.build(ctx, passThrough, build.Env{Signature: anySignature, Declarations: "declare module \"x\" {}"}); &withDecls[0] == &first[0] {
		t.Error("other declarations gave the cached module")
	}
	// That evicted the first module, as the cache holds one.
	if again, _ := c.build(ctx, passThrough, env); &again[0] == &first[0] {
		t.Error("the first build was not evicted")
	}

	// Source that does not build is cached too.
	_, err1 := c.build(ctx, other+"{", env)
	_, err2 := c.build(ctx, other+"{", env)
	var invalid *build.InvalidSourceError
	if !errors.As(err1, &invalid) || err1 != err2 {
		t.Errorf("got %v and %v, want the same InvalidSourceError", err1, err2)
	}

	// Other errors are not.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.build(cancelled, other, env); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want Canceled", err)
	}
	if _, err := c.build(ctx, other, env); err != nil {
		t.Errorf("after a cancelled build: %v", err)
	}
}

func TestBuildKey(t *testing.T) {
	// Moving bytes between the fields changes the key.
	a := buildKey("ab", build.Env{Declarations: "c"})
	b := buildKey("a", build.Env{Declarations: "bc"})
	if a == b {
		t.Error("different inputs have the same key")
	}
}

func TestEngineCache(t *testing.T) {
	ctx := context.Background()
	// Without dependencies, engines are quick to create.
	c := newEngineCache(nil, 1)
	defer c.close()
	keyA := engineKey{memoryMiB: 1, closeOnContextDone: true}
	keyB := engineKey{memoryMiB: 2, closeOnContextDone: true}
	keyC := engineKey{memoryMiB: 3, closeOnContextDone: true}

	a1, releaseA1, err := c.acquire(ctx, keyA)
	if err != nil {
		t.Fatal(err)
	}
	a2, releaseA2, _ := c.acquire(ctx, keyA)
	if a1 != a2 {
		t.Error("the same key gave two engines")
	}
	b, releaseB, _ := c.acquire(ctx, keyB)
	if b == a1 {
		t.Error("another key gave the same engine")
	}

	// A and B are in use, so they stay although the cache keeps one idle.
	releaseA1()
	releaseA1() // Releasing twice is harmless.
	if a3, releaseA3, _ := c.acquire(ctx, keyA); a3 != a1 {
		t.Error("an engine in use was evicted")
	} else {
		releaseA3()
	}

	// When A and B are idle, the least recently used one goes.
	releaseA2()
	releaseB()
	if b2, release, _ := c.acquire(ctx, keyB); b2 != b {
		t.Error("the most recently used idle engine was evicted")
	} else {
		release()
	}
	if a4, release, _ := c.acquire(ctx, keyA); a4 == a1 {
		t.Error("the least recently used idle engine was kept")
	} else {
		release()
	}
	// The evicted engine is closed.
	if _, err := a1.Load(ctx, nil); err == nil {
		t.Error("the evicted engine is still open")
	}

	// A context that is done while waiting for an engine stops waiting.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := c.acquire(cancelled, keyC); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want Canceled", err)
	}
}

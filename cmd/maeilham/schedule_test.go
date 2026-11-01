package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunEvery_RunsImmediatelyThenRepeatsUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	done := make(chan struct{})

	go func() {
		runEvery(ctx, 10*time.Millisecond, func(context.Context) {
			if calls.Add(1) == 3 {
				cancel()
			}
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runEvery did not return after cancel")
	}
	if got := calls.Load(); got < 3 {
		t.Fatalf("calls = %d, want >= 3", got)
	}
}

func TestRunEvery_DoesNotOverlapSlowRuns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var running, maxRunning atomic.Int32

	runEvery(ctx, 5*time.Millisecond, func(context.Context) {
		n := running.Add(1)
		if n > maxRunning.Load() {
			maxRunning.Store(n)
		}
		time.Sleep(30 * time.Millisecond)
		running.Add(-1)
	})

	if got := maxRunning.Load(); got != 1 {
		t.Fatalf("max concurrent runs = %d, want 1", got)
	}
}

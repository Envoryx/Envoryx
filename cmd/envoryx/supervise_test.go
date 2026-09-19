package main

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestSuperviseRestartsAfterPanic(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var runs atomic.Int32
	done := make(chan struct{})
	go func() {
		supervise(ctx, log, nil, "test", func(ctx context.Context) {
			if runs.Add(1) < 3 {
				panic("boom")
			}
			close(done) // third run returns normally → supervise stops
		})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("task was not restarted after panics")
	}
	if runs.Load() != 3 {
		t.Fatalf("runs = %d", runs.Load())
	}
}

func TestSuperviseStopsWhenContextEnds(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	var runs atomic.Int32
	finished := make(chan struct{})
	go func() {
		supervise(ctx, log, nil, "test", func(context.Context) {
			runs.Add(1)
			panic("always")
		})
		close(finished)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("supervise did not stop")
	}
	if runs.Load() < 1 {
		t.Fatal("task never ran")
	}
}

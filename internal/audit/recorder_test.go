package audit

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestRecord_StampsTimestampWhenZero(t *testing.T) {
	var got Event
	prev := SetDefault(RecorderFunc(func(_ context.Context, e Event) { got = e }))
	t.Cleanup(func() { SetDefault(prev) })

	Record(context.Background(), Event{Action: ActionUserCreated})

	if got.Timestamp.IsZero() {
		t.Fatal("expected Record to stamp Timestamp when zero")
	}
	if time.Since(got.Timestamp) > time.Second {
		t.Fatalf("Timestamp should be near-now, got %v", got.Timestamp)
	}
}

func TestRecord_PreservesProvidedTimestamp(t *testing.T) {
	var got Event
	prev := SetDefault(RecorderFunc(func(_ context.Context, e Event) { got = e }))
	t.Cleanup(func() { SetDefault(prev) })

	want := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	Record(context.Background(), Event{Action: ActionUserCreated, Timestamp: want})

	if !got.Timestamp.Equal(want) {
		t.Fatalf("Timestamp = %v, want %v", got.Timestamp, want)
	}
}

func TestSetDefault_NilRevertsToNoop(t *testing.T) {
	prev := SetDefault(RecorderFunc(func(context.Context, Event) {
		t.Fatal("real recorder should have been replaced by noop")
	}))
	SetDefault(nil)
	t.Cleanup(func() { SetDefault(prev) })

	// No assertion — passing is "did not panic, did not call the old recorder".
	Record(context.Background(), Event{Action: ActionRoleCreated})
}

func TestRecord_ConcurrentSafe(t *testing.T) {
	var (
		mu    sync.Mutex
		count int
	)
	prev := SetDefault(RecorderFunc(func(_ context.Context, _ Event) {
		mu.Lock()
		count++
		mu.Unlock()
	}))
	t.Cleanup(func() { SetDefault(prev) })

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			Record(context.Background(), Event{Action: ActionUserUpdated})
		}()
	}
	wg.Wait()

	if count != n {
		t.Fatalf("count = %d, want %d", count, n)
	}
}

func TestDefault_ReturnsCurrent(t *testing.T) {
	want := RecorderFunc(func(context.Context, Event) {})
	prev := SetDefault(want)
	t.Cleanup(func() { SetDefault(prev) })

	if Default() == nil {
		t.Fatal("Default() returned nil")
	}
}

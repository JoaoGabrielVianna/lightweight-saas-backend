package audit

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestMemoryRecorder_StoresAndReturnsNewestFirst(t *testing.T) {
	m := NewMemoryRecorder(5)
	ctx := context.Background()

	base := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		m.Record(ctx, Event{
			Action:    ActionUserCreated,
			Target:    Target{Kind: "user", ID: "u" + string(rune('A'+i))},
			Timestamp: base.Add(time.Duration(i) * time.Second),
		})
	}

	snap, dropped := m.Snapshot(0, time.Time{})
	if dropped != 0 {
		t.Fatalf("dropped = %d, want 0", dropped)
	}
	if len(snap) != 3 {
		t.Fatalf("len(snap) = %d, want 3", len(snap))
	}
	// Newest-first: u_C, u_B, u_A.
	want := []string{"uC", "uB", "uA"}
	for i, w := range want {
		if snap[i].Target.ID != w {
			t.Fatalf("snap[%d].Target.ID = %q, want %q", i, snap[i].Target.ID, w)
		}
	}
}

func TestMemoryRecorder_OverwritesOldest(t *testing.T) {
	m := NewMemoryRecorder(3)
	ctx := context.Background()

	base := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		m.Record(ctx, Event{
			Action:    ActionUserUpdated,
			Target:    Target{Kind: "user", ID: string(rune('A' + i))},
			Timestamp: base.Add(time.Duration(i) * time.Second),
		})
	}

	snap, dropped := m.Snapshot(0, time.Time{})
	if len(snap) != 3 {
		t.Fatalf("len(snap) = %d, want 3", len(snap))
	}
	if dropped != 2 {
		t.Fatalf("dropped = %d, want 2", dropped)
	}
	// Newest-first should be E, D, C — A and B were overwritten.
	want := []string{"E", "D", "C"}
	for i, w := range want {
		if snap[i].Target.ID != w {
			t.Fatalf("snap[%d].Target.ID = %q, want %q", i, snap[i].Target.ID, w)
		}
	}
}

func TestMemoryRecorder_LimitAndSince(t *testing.T) {
	m := NewMemoryRecorder(10)
	ctx := context.Background()

	base := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		m.Record(ctx, Event{
			Action:    ActionRoleCreated,
			Target:    Target{Kind: "role", ID: string(rune('A' + i))},
			Timestamp: base.Add(time.Duration(i) * time.Second),
		})
	}

	// limit
	snap, _ := m.Snapshot(2, time.Time{})
	if len(snap) != 2 {
		t.Fatalf("len(snap) with limit=2 = %d, want 2", len(snap))
	}

	// since — strictly after means base+1s and onwards are excluded if
	// since == base+1s. Newest-first events after base+2s are: E, D.
	cutoff := base.Add(2 * time.Second)
	snap, _ = m.Snapshot(0, cutoff)
	if len(snap) != 2 {
		t.Fatalf("len(snap) with since=base+2s = %d, want 2 (E,D)", len(snap))
	}
	if snap[0].Target.ID != "E" || snap[1].Target.ID != "D" {
		t.Fatalf("since filter returned %v %v, want E,D", snap[0].Target.ID, snap[1].Target.ID)
	}
}

func TestMemoryRecorder_StampsZeroTimestamp(t *testing.T) {
	m := NewMemoryRecorder(2)
	m.Record(context.Background(), Event{Action: ActionUserCreated})

	snap, _ := m.Snapshot(0, time.Time{})
	if len(snap) != 1 {
		t.Fatalf("len(snap) = %d, want 1", len(snap))
	}
	if snap[0].Timestamp.IsZero() {
		t.Fatal("expected MemoryRecorder to stamp Timestamp when zero")
	}
	if snap[0].Timestamp.Location() != time.UTC {
		t.Fatalf("Timestamp location = %v, want UTC", snap[0].Timestamp.Location())
	}
}

func TestMemoryRecorder_EmptySnapshot(t *testing.T) {
	m := NewMemoryRecorder(4)
	snap, dropped := m.Snapshot(0, time.Time{})
	if len(snap) != 0 {
		t.Fatalf("len(snap) on empty = %d, want 0", len(snap))
	}
	if dropped != 0 {
		t.Fatalf("dropped on empty = %d, want 0", dropped)
	}
}

func TestMemoryRecorder_ClampsCapacityToOne(t *testing.T) {
	m := NewMemoryRecorder(0)
	if got := m.Capacity(); got != 1 {
		t.Fatalf("Capacity() = %d, want 1 (clamped)", got)
	}
}

func TestMemoryRecorder_ConcurrentRecord(t *testing.T) {
	m := NewMemoryRecorder(1000)
	const writers = 50
	const each = 20

	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				m.Record(context.Background(), Event{Action: ActionUserUpdated})
			}
		}()
	}
	wg.Wait()

	snap, _ := m.Snapshot(0, time.Time{})
	if len(snap) != writers*each {
		t.Fatalf("len(snap) = %d, want %d", len(snap), writers*each)
	}
}

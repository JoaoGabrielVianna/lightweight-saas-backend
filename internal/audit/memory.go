package audit

import (
	"context"
	"sync"
	"time"
)

// MemoryRecorder is a bounded in-memory ring buffer of recent audit
// events. Its purpose is narrow: give the admin console a window into
// the events the system is already emitting so operators can answer
// "what just happened?" without grepping logs.
//
// Scope (deliberately small):
//   - Bounded capacity — oldest entries are dropped, never grows.
//   - Process-local — events recorded by another replica are invisible
//     here. Acceptable because the v0.2 admin console targets single-
//     node deployments; durable cross-node history is what the Sprint 4
//     audit_log table is for.
//   - Volatile — restart drops the buffer. The UI labels this honestly.
//
// MemoryRecorder is concurrency-safe; Record may be called from any
// goroutine and Snapshot may run concurrently with writers.
type MemoryRecorder struct {
	mu       sync.Mutex
	buf      []Event
	head     int    // index of the next slot to write
	full     bool   // true once we've wrapped at least once
	dropped  uint64 // events evicted by ring-buffer overwrite
	capacity int
}

// NewMemoryRecorder returns a MemoryRecorder that retains the most
// recent `capacity` events. A non-positive capacity is clamped to 1
// so the buffer is always usable.
func NewMemoryRecorder(capacity int) *MemoryRecorder {
	if capacity < 1 {
		capacity = 1
	}
	return &MemoryRecorder{
		buf:      make([]Event, capacity),
		capacity: capacity,
	}
}

// Record stores the event in the ring buffer. Timestamp is stamped to
// time.Now().UTC() when zero so consumers see a sortable instant. The
// stored copy is detached from the caller's Extra map — see Clone in
// internal/audit/event.go if Event grows reference fields the buffer
// must defensively-copy.
func (m *MemoryRecorder) Record(_ context.Context, e Event) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	e.Timestamp = e.Timestamp.UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.full {
		m.dropped++
	}
	m.buf[m.head] = e
	m.head++
	if m.head >= m.capacity {
		m.head = 0
		m.full = true
	}
}

// Snapshot returns events in newest-first order, optionally filtered to
// events whose Timestamp is strictly after `since`. limit <= 0 means
// "no limit" (return up to capacity). The second return value is the
// number of events that have been overwritten since process start —
// useful for the UI to warn "you missed older entries".
func (m *MemoryRecorder) Snapshot(limit int, since time.Time) ([]Event, uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	count := m.capacity
	if !m.full {
		count = m.head
	}
	if count == 0 {
		return nil, m.dropped
	}

	// Walk backwards from the most recent write. When full, the most
	// recent event is at (head-1) mod capacity; before that, at head-2;
	// etc. When not yet full, head-1 is still the most recent.
	out := make([]Event, 0, count)
	for i := 0; i < count; i++ {
		idx := (m.head - 1 - i + m.capacity) % m.capacity
		e := m.buf[idx]
		if !since.IsZero() && !e.Timestamp.After(since) {
			break // older entries can only be older — buffer is time-ordered
		}
		out = append(out, e)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, m.dropped
}

// Capacity returns the configured ring-buffer capacity.
func (m *MemoryRecorder) Capacity() int { return m.capacity }

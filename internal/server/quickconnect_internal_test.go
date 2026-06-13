package server

import (
	"errors"
	"testing"
	"time"
)

// TestQuickConnectStoreCap verifies the pending-request cap bounds memory, that
// expired requests free a slot, and that a consumed/expired secret is gone.
func TestQuickConnectStoreCap(t *testing.T) {
	q := newQuickConnectStore()

	for i := 0; i < quickConnectMaxPending; i++ {
		if _, err := q.initiate("dev", "name", "app", "1.0"); err != nil {
			t.Fatalf("initiate %d: %v", i, err)
		}
	}

	// The next request past the cap is rejected rather than growing the store.
	if _, err := q.initiate("dev", "name", "app", "1.0"); !errors.Is(err, errQuickConnectFull) {
		t.Fatalf("over-cap initiate err = %v, want errQuickConnectFull", err)
	}

	// Expiring the existing requests frees capacity again.
	q.mu.Lock()
	for _, req := range q.reqs {
		req.added = time.Now().Add(-2 * quickConnectTTL)
	}
	q.mu.Unlock()

	if _, err := q.initiate("dev", "name", "app", "1.0"); err != nil {
		t.Fatalf("initiate after expiry: %v", err)
	}
	q.mu.Lock()
	n := len(q.reqs)
	q.mu.Unlock()
	if n != 1 {
		t.Fatalf("store size after expiry = %d, want 1", n)
	}
}

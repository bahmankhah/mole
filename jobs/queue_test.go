package jobs

import "testing"

func TestEnqueueLockedDedup(t *testing.T) {
	m := &Manager{}
	m.enqueueLocked("a")
	m.enqueueLocked("a")
	m.enqueueLocked("b")
	if len(m.queuedIDs) != 2 {
		t.Fatalf("queued=%v", m.queuedIDs)
	}
	if m.queuedIDs[0] != "a" || m.queuedIDs[1] != "b" {
		t.Fatalf("queued=%v", m.queuedIDs)
	}
}

func TestRemoveQueuedLocked(t *testing.T) {
	m := &Manager{}
	m.enqueueLocked("a")
	m.enqueueLocked("b")
	m.enqueueLocked("c")
	m.removeQueuedLocked("b")
	if len(m.queuedIDs) != 2 || m.queuedIDs[0] != "a" || m.queuedIDs[1] != "c" {
		t.Fatalf("queued=%v", m.queuedIDs)
	}
	m.removeQueuedLocked("missing")
	if len(m.queuedIDs) != 2 {
		t.Fatalf("queued=%v", m.queuedIDs)
	}
	m.removeQueuedLocked("a")
	m.removeQueuedLocked("c")
	if len(m.queuedIDs) != 0 {
		t.Fatalf("queued=%v", m.queuedIDs)
	}
}

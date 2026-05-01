package ac

import "sync"

// commandEvent replicates ManualResetEvent(false) broadcast semantics.
// Set() closes the channel (all waiters unblock). Reset() replaces it.
// Unlike a channel send, Set is idempotent and all concurrent readers unblock.
type commandEvent struct {
	mu sync.Mutex
	ch chan struct{}
}

func newCommandEvent() *commandEvent {
	return &commandEvent{ch: make(chan struct{})}
}

func (e *commandEvent) Set() {
	e.mu.Lock()
	defer e.mu.Unlock()
	select {
	case <-e.ch:
		// already closed
	default:
		close(e.ch)
	}
}

func (e *commandEvent) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	select {
	case <-e.ch:
		// was closed — replace with new open channel
		e.ch = make(chan struct{})
	default:
		// already open — nothing to do
	}
}

// C returns the current channel. Caller must capture this before select.
func (e *commandEvent) C() <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ch
}

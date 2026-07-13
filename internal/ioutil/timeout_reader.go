package ioutil

import (
	"io"
	"sync"
	"time"
)

// IdleTimeoutReader wraps an io.Reader and closes it if no data arrives within idleTimeout.
// This prevents goroutine leaks when a remote connection stalls (sends headers but
// never closes the connection or sends more data).
type IdleTimeoutReader struct {
	r           io.Reader
	c           io.Closer
	idleTimeout time.Duration
	mu          sync.Mutex
	lastRead    time.Time
	closed      bool
	timedOut    bool
	stopMon     chan struct{}
}

// NewIdleTimeoutReader wraps r with idle timeout detection.
// If r implements io.Closer, it is closed when the timeout fires.
func NewIdleTimeoutReader(r io.Reader, idleTimeout time.Duration) *IdleTimeoutReader {
	var c io.Closer
	if cl, ok := r.(io.Closer); ok {
		c = cl
	}
	tr := &IdleTimeoutReader{
		r:           r,
		c:           c,
		idleTimeout: idleTimeout,
		lastRead:    time.Now(),
		stopMon:     make(chan struct{}),
	}
	go tr.monitor()
	return tr
}

func (t *IdleTimeoutReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	t.mu.Lock()
	t.lastRead = time.Now()
	t.mu.Unlock()
	return n, err
}

func (t *IdleTimeoutReader) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	close(t.stopMon)
	if t.c != nil {
		return t.c.Close()
	}
	return nil
}

// IsTimedOut reports whether the reader was closed due to idle timeout.
func (t *IdleTimeoutReader) IsTimedOut() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.timedOut
}

func (t *IdleTimeoutReader) monitor() {
	ticker := time.NewTicker(t.idleTimeout / 2)
	defer ticker.Stop()
	for {
		select {
		case <-t.stopMon:
			return
		case <-ticker.C:
			t.mu.Lock()
			elapsed := time.Since(t.lastRead)
			shouldClose := elapsed >= t.idleTimeout && !t.closed
			t.timedOut = shouldClose
			t.mu.Unlock()
			if shouldClose {
				_ = t.Close()
				return
			}
		}
	}
}

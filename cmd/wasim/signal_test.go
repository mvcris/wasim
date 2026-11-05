//go:build unix

package main

import (
	"bytes"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// syncBuffer lets the test read the log while the server writes it.
type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func TestRunsUntilInterrupted(t *testing.T) {
	t.Setenv("WASIM_GRAPH_ADDR", "127.0.0.1:0")
	t.Setenv("WASIM_CONSOLE_ADDR", "127.0.0.1:0")
	t.Setenv("WASIM_DB_PATH", ":memory:")
	t.Setenv("WASIM_MEDIA_DIR", t.TempDir())
	var stdout bytes.Buffer
	var stderr syncBuffer

	done := make(chan error, 1)
	go func() { done <- run(nil, &stdout, &stderr) }()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(stderr.String(), "wasim is running") {
		if time.Now().After(deadline) {
			t.Fatalf("wasim never started: %s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v after an interrupt", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wasim did not stop on SIGINT")
	}
}

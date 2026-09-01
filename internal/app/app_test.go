package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"nextnet/internal/serial"
)

func TestAppClosesSerialForAnyNonTargetCore(t *testing.T) {
	coreFile := filepath.Join(t.TempDir(), "CORENAME")
	if err := os.WriteFile(coreFile, []byte("ZXNext\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	runner := New(Config{
		Device:        "/dev/fake",
		Baud:          115200,
		Core:          "ZXNext",
		CoreFile:      coreFile,
		PollInterval:  5 * time.Millisecond,
		RetryInterval: 5 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(&logs, nil)))

	opened := make(chan *blockingTransport, 2)
	runner.resolve = func(device string) (string, error) { return device, nil }
	runner.open = func(string, int) (serial.Transport, error) {
		transport := newBlockingTransport()
		opened <- transport
		return transport, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	var transport *blockingTransport
	select {
	case transport = <-opened:
	case <-time.After(time.Second):
		t.Fatal("serial device was not opened for ZXNext")
	}

	if err := os.WriteFile(coreFile, []byte("AlternativeLauncher\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-transport.closed:
	case <-time.After(time.Second):
		t.Fatal("serial device was not closed for a non-ZXNext core name")
	}

	if err := os.WriteFile(coreFile, []byte("ZXNext\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var reopened *blockingTransport
	select {
	case reopened = <-opened:
	case <-time.After(time.Second):
		t.Fatal("serial device was not reopened after returning to ZXNext")
	}

	cancel()
	select {
	case <-reopened.closed:
	case <-time.After(time.Second):
		t.Fatal("serial device was not closed during daemon shutdown")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}
}

type blockingTransport struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingTransport() *blockingTransport {
	return &blockingTransport{closed: make(chan struct{})}
}

func (t *blockingTransport) Read([]byte) (int, error) {
	<-t.closed
	return 0, io.EOF
}

func (t *blockingTransport) Write(data []byte) (int, error) {
	select {
	case <-t.closed:
		return 0, io.ErrClosedPipe
	default:
		return len(data), nil
	}
}

func (t *blockingTransport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}

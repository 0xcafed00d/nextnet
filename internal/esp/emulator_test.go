package esp

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

func TestEmulatorBasicCommandsAndEcho(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	emulatorSide, clientSide := net.Pipe()
	defer clientSide.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- New(logger, "test").Serve(ctx, emulatorSide) }()

	testExchange(t, clientSide, "AT\r\n", "AT\r\n\r\nOK\r\n")
	testExchange(t, clientSide, "ATE0\r\n", "ATE0\r\n\r\nOK\r\n")
	testExchange(t, clientSide, "AT\r\n", "\r\nOK\r\n")
	testExchange(t, clientSide, "ATE1\r\n", "\r\nOK\r\n")
	testExchange(t, clientSide, "AT+NOPE\r\n", "AT+NOPE\r\n\r\nERROR\r\n")
	testExchange(t, clientSide, "AT+RST\r\n", "AT+RST\r\n\r\nOK\r\n\r\nready\r\n")
	testExchange(t, clientSide, "AT\r\n", "AT\r\n\r\nOK\r\n")
	testExchange(t, clientSide, "AT\x00\r\n", "\r\nERROR\r\n")

	cancel()
	_ = clientSide.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop after cancellation")
	}
}

func TestEmulatorLogsCommandsAndRedactsCredentials(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	emulator := New(logger, "test")

	events := newLineParser(maximumATLine).feed([]byte(`AT+CWJAP="network","secret"` + "\r\n"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := make(chan []byte, 4)
	if err := emulator.processEvent(ctx, output, events[0]); err != nil {
		t.Fatal(err)
	}

	text := logs.String()
	if !strings.Contains(text, "AT+CWJAP=<redacted>") {
		t.Fatalf("redacted command missing from log: %s", text)
	}
	if strings.Contains(text, "network") || strings.Contains(text, "secret") {
		t.Fatalf("credentials leaked into log: %s", text)
	}
	if !strings.Contains(text, "AT unsupported") {
		t.Fatalf("unsupported marker missing from log: %s", text)
	}
}

func testExchange(t *testing.T, connection net.Conn, command, want string) {
	t.Helper()
	if err := connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte(command)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(connection, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("response to %q = %q, want %q", command, got, want)
	}
}

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
	go func() { done <- New(logger, Config{Version: "test", Baud: 115200}).Serve(ctx, emulatorSide) }()

	testExchange(t, clientSide, "AT\r\n", "AT\r\n\r\nOK\r\n")
	testExchange(t, clientSide, "ATE0\r\n", "ATE0\r\n\r\nOK\r\n")
	testExchange(t, clientSide, "AT\r\n", "\r\nOK\r\n")
	testExchange(t, clientSide, "ATE1\r\n", "\r\nOK\r\n")
	testExchange(t, clientSide, "AT+NOPE\r\n", "AT+NOPE\r\n\r\nERROR\r\n")
	testExchange(t, clientSide, "AT+RST\r\n", "AT+RST\r\n\r\nOK\r\nWIFI CONNECTED\r\nWIFI GOT IP\r\n\r\nready\r\n")
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
	emulator := New(logger, Config{Version: "test", Baud: 115200})

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

func TestEmulatorSingleConnectionTCP(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	serverConnections := make(chan net.Conn, 2)
	dialer := espDialerFunc(func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" {
			t.Fatalf("network = %q, want tcp", network)
		}
		if address != "example.com:80" {
			t.Fatalf("address = %q, want example.com:80", address)
		}
		client, server := net.Pipe()
		serverConnections <- server
		return client, nil
	})

	emulatorSide, clientSide := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- New(logger, Config{
			Version: "test",
			Baud:    115200,
			Dialer:  dialer,
		}).Serve(ctx, emulatorSide)
	}()

	testExchange(t, clientSide, "ATE0\r\n", "ATE0\r\n\r\nOK\r\n")
	testExchange(t, clientSide, "AT+UART_CUR?\r\n", "\r\n+UART_CUR:115200,8,1,0,0\r\n\r\nOK\r\n")
	testExchange(t, clientSide, "AT+CIPMUX=0\r\n", "\r\nOK\r\n")
	testExchange(t, clientSide, "AT+CIPCLOSE\r\n", "\r\nERROR\r\n")
	testExchange(t, clientSide, `AT+CIPSTART="TCP","example.com",80`+"\r\n", "\r\nCONNECT\r\n\r\nOK\r\n")
	server := <-serverConnections

	payload := []byte{0x00, 0xff, '\r', '\n', 'A'}
	received := make(chan []byte, 1)
	go func() {
		got := make([]byte, len(payload))
		_, _ = io.ReadFull(server, got)
		received <- got
		_, _ = server.Write([]byte("WORLD"))
		_ = server.Close()
	}()

	if err := clientSide.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	commandAndPayload := append([]byte("AT+CIPSEND=5\r\n"), payload...)
	if _, err := clientSide.Write(commandAndPayload); err != nil {
		t.Fatal(err)
	}
	want := "\r\nOK\r\n>\r\nSEND OK\r\n+IPD,5:WORLD\r\nCLOSED\r\n"
	got := make([]byte, len(want))
	if _, err := io.ReadFull(clientSide, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("TCP exchange UART output = %q, want %q", got, want)
	}
	if gotPayload := <-received; !bytes.Equal(gotPayload, payload) {
		t.Fatalf("TCP payload = %v, want %v", gotPayload, payload)
	}

	// A locally requested close returns a synchronous response and suppresses a
	// duplicate unsolicited CLOSED notification from the reader goroutine.
	testExchange(t, clientSide, `AT+CIPSTART="TCP","example.com",80`+"\r\n", "\r\nCONNECT\r\n\r\nOK\r\n")
	server = <-serverConnections
	testExchange(t, clientSide, "AT+CIPCLOSE\r\n", "\r\nCLOSED\r\n\r\nOK\r\n")
	_ = server.Close()

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

	logText := logs.String()
	if !strings.Contains(logText, "CIPSEND payload received") || !strings.Contains(logText, "bytes=5") {
		t.Fatalf("payload length missing from logs: %s", logText)
	}
	if strings.Contains(logText, "WORLD") {
		t.Fatalf("network payload leaked into logs: %s", logText)
	}
}

func TestEmulatorCancellationInterruptsBlockedSocketSend(t *testing.T) {
	serverConnections := make(chan net.Conn, 1)
	dialer := espDialerFunc(func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		serverConnections <- server
		return client, nil
	})
	emulatorSide, clientSide := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- New(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
			Dialer: dialer,
		}).Serve(ctx, emulatorSide)
	}()

	testExchange(t, clientSide, "ATE0\r\n", "ATE0\r\n\r\nOK\r\n")
	testExchange(t, clientSide, `AT+CIPSTART="TCP","blocked.test",80`+"\r\n", "\r\nCONNECT\r\n\r\nOK\r\n")
	server := <-serverConnections
	defer server.Close()

	if err := clientSide.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := clientSide.Write([]byte("AT+CIPSEND=4\r\nDATA")); err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = clientSide.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not cancel a blocked socket send")
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

type espDialerFunc func(context.Context, string, string) (net.Conn, error)

func (function espDialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return function(ctx, network, address)
}

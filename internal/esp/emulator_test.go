package esp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"nextnet/internal/sockets"
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

func TestAsyncFramesUseConnectionFramingMode(t *testing.T) {
	emulator := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{})
	emulator.mux = false
	output := make(chan []byte, 2)
	link := sockets.Link{ID: 0, Multiplexed: true}

	emulator.emitIPD(context.Background(), output, link, []byte("DATA"))
	emulator.emitClosed(context.Background(), output, link)
	if got := string(<-output); got != "+IPD,0,4:DATA" {
		t.Fatalf("delayed mux IPD = %q", got)
	}
	if got := string(<-output); got != "\r\n0,CLOSED\r\n" {
		t.Fatalf("delayed mux close = %q", got)
	}
}

func TestCIPDInfoAddsRemoteEndpointToIPD(t *testing.T) {
	emulator := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{})
	emulator.cipdInfo = true
	output := make(chan []byte, 1)
	link := sockets.Link{ID: 3, Multiplexed: true, RemoteHost: "192.0.2.4", RemotePort: 8080}

	emulator.emitIPD(context.Background(), output, link, []byte("DATA"))
	if got := string(<-output); got != "+IPD,3,4,192.0.2.4,8080:DATA" {
		t.Fatalf("CIPDINFO frame = %q", got)
	}
}

func TestRemoteCloseLeavesTransparentMode(t *testing.T) {
	emulator := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{})
	emulator.transparentActive.Store(true)
	output := make(chan []byte, 1)

	emulator.emitClosed(context.Background(), output, sockets.Link{ID: 0})
	if emulator.transparentActive.Load() {
		t.Fatal("transparent mode remained active after remote close")
	}
	if got := string(<-output); got != "\r\nCLOSED\r\n" {
		t.Fatalf("remote close frame = %q", got)
	}
}

func TestTransparentEscapeRequiresGuardIntervals(t *testing.T) {
	emulator := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{})
	start := time.Unix(100, 0)
	emulator.transparentActive.Store(true)
	emulator.transparentLastByte = start
	payload := make([]byte, 0, 8)

	// Three pluses immediately following payload remain ordinary data.
	for index, value := range []byte("A+++") {
		consumed, escaped := emulator.consumeTransparentByte(value, &payload, start.Add(time.Duration(index+1)*time.Millisecond))
		if !consumed || escaped {
			t.Fatalf("ordinary byte %d = consumed %v escaped %v", index, consumed, escaped)
		}
	}
	if got := string(payload); got != "A+++" {
		t.Fatalf("ordinary plus payload = %q", got)
	}

	payload = payload[:0]
	firstPlus := start.Add(50 * time.Millisecond)
	for index := 0; index < 3; index++ {
		consumed, escaped := emulator.consumeTransparentByte('+', &payload, firstPlus.Add(time.Duration(index)*time.Millisecond))
		if !consumed || escaped {
			t.Fatalf("escape plus %d = consumed %v escaped %v", index, consumed, escaped)
		}
	}
	consumed, escaped := emulator.consumeTransparentByte('A', &payload, firstPlus.Add(30*time.Millisecond))
	if consumed || !escaped || emulator.transparentActive.Load() {
		t.Fatalf("guarded escape = consumed %v escaped %v active %v", consumed, escaped, emulator.transparentActive.Load())
	}
	if len(payload) != 0 {
		t.Fatalf("escape sequence leaked into payload: %q", payload)
	}
}

func TestEmulatorTransparentTCP(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	serverConnections := make(chan net.Conn, 1)
	dialer := espDialerFunc(func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "bbs.zxnext.uk:2323" {
			t.Fatalf("dial = %q %q, want tcp bbs.zxnext.uk:2323", network, address)
		}
		client, server := net.Pipe()
		serverConnections <- server
		return client, nil
	})

	emulatorSide, clientSide := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- New(logger, Config{Version: "test", Baud: 115200, Dialer: dialer}).Serve(ctx, emulatorSide)
	}()

	testExchange(t, clientSide, "ATE0\r\n", "ATE0\r\n\r\nOK\r\n")
	testExchange(t, clientSide, "AT+CIPDINFO=0\r\n", "\r\nOK\r\n")
	testExchange(t, clientSide, "AT+CIPRECVMODE=1\r\n", "\r\nOK\r\n")
	testExchange(t, clientSide, "AT+CIPRECVMODE?\r\n", "\r\n+CIPRECVMODE:1\r\n\r\nOK\r\n")
	testExchange(t, clientSide, "AT+CIPMUX=0\r\n", "\r\nOK\r\n")
	testExchange(t, clientSide, "AT+CIPMODE=1\r\n", "\r\nOK\r\n")
	testExchange(t, clientSide, `AT+CIPSTART="TCP","bbs.zxnext.uk",2323,1`+"\r\n", "\r\nCONNECT\r\n\r\nOK\r\n")
	server := <-serverConnections
	defer server.Close()

	// CIPMODE=1 enables transparent receiving even before bare CIPSEND turns
	// on transparent UART-to-socket transmission.
	go func() { _, _ = server.Write([]byte("PRE")) }()
	readExact(t, clientSide, "PRE")
	testExchange(t, clientSide, "AT+CIPSEND\r\n", "\r\nOK\r\n>")

	go func() { _, _ = server.Write([]byte("WELCOME")) }()
	readExact(t, clientSide, "WELCOME")

	received := make(chan []byte, 1)
	go func() {
		payload := make([]byte, len("HELLO++X"))
		_, _ = io.ReadFull(server, payload)
		received <- payload
	}()
	if _, err := clientSide.Write([]byte("HELLO++")); err != nil {
		t.Fatal(err)
	}
	if _, err := clientSide.Write([]byte("X")); err != nil {
		t.Fatal(err)
	}
	if got := string(<-received); got != "HELLO++X" {
		t.Fatalf("transparent TCP payload = %q", got)
	}

	time.Sleep(transparentGuard + 5*time.Millisecond)
	if _, err := clientSide.Write([]byte("+++")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(transparentGuard + 5*time.Millisecond)
	go func() { _, _ = server.Write([]byte("AFTER")) }()
	readExact(t, clientSide, "AFTER")
	testExchange(t, clientSide, "AT+CIPMODE?\r\n", "\r\n+CIPMODE:1\r\n\r\nOK\r\n")
	testExchange(t, clientSide, "AT+CIPCLOSE\r\n", "\r\nCLOSED\r\n\r\nOK\r\n")

	cancel()
	_ = clientSide.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop after transparent exchange")
	}

	logText := logs.String()
	for _, fragment := range []string{"transparent mode entered", "transparent socket sent", "transparent=true", "transparent mode exited"} {
		if !strings.Contains(logText, fragment) {
			t.Fatalf("transparent log is missing %q: %s", fragment, logText)
		}
	}
	if strings.Contains(logText, "WELCOME") || strings.Contains(logText, "HELLO") {
		t.Fatalf("transparent payload leaked into logs: %s", logText)
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

func TestEmulatorMultiplexedTCP(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	type acceptedConnection struct {
		address string
		conn    net.Conn
	}
	serverConnections := make(chan acceptedConnection, 2)
	dialer := espDialerFunc(func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" {
			t.Fatalf("network = %q, want tcp", network)
		}
		client, server := net.Pipe()
		serverConnections <- acceptedConnection{address: address, conn: server}
		return client, nil
	})

	emulatorSide, clientSide := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- New(logger, Config{Version: "test", Baud: 115200, Dialer: dialer}).Serve(ctx, emulatorSide)
	}()

	testExchange(t, clientSide, "ATE0\r\n", "ATE0\r\n\r\nOK\r\n")
	testExchange(t, clientSide, "AT+CIPSTATUS\r\n", "\r\nSTATUS:2\r\n\r\nOK\r\n")
	testExchange(t, clientSide, "AT+CIPMUX=1\r\n", "\r\nOK\r\n")
	testExchange(t, clientSide, "AT+CIPMUX?\r\n", "\r\n+CIPMUX:1\r\n\r\nOK\r\n")

	testExchange(t, clientSide, `AT+CIPSTART=2,"TCP","two.test",2002`+"\r\n", "\r\n2,CONNECT\r\n\r\nOK\r\n")
	peer2 := <-serverConnections
	if peer2.address != "two.test:2002" {
		t.Fatalf("ID 2 dialed %q", peer2.address)
	}
	testExchange(t, clientSide, `AT+CIPSTART=4,"TCP","four.test",4004`+"\r\n", "\r\n4,CONNECT\r\n\r\nOK\r\n")
	peer4 := <-serverConnections
	if peer4.address != "four.test:4004" {
		t.Fatalf("ID 4 dialed %q", peer4.address)
	}

	wantStatus := "\r\nSTATUS:3\r\n" +
		`+CIPSTATUS:2,"TCP","two.test",2002,0,0` + "\r\n" +
		`+CIPSTATUS:4,"TCP","four.test",4004,0,0` + "\r\n\r\nOK\r\n"
	testExchange(t, clientSide, "AT+CIPSTATUS\r\n", wantStatus)
	testExchange(t, clientSide, "AT+CIPMUX=0\r\n", "\r\nERROR\r\n")

	payload := []byte{0x00, 0xff, 'T', 'W', 'O'}
	received := make(chan []byte, 1)
	go func() {
		got := make([]byte, len(payload))
		_, _ = io.ReadFull(peer2.conn, got)
		received <- got
	}()
	if err := clientSide.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	commandAndPayload := append([]byte("AT+CIPSEND=2,5\r\n"), payload...)
	if _, err := clientSide.Write(commandAndPayload); err != nil {
		t.Fatal(err)
	}
	wantSend := "\r\nOK\r\n>\r\nSEND OK\r\n"
	gotSend := make([]byte, len(wantSend))
	if _, err := io.ReadFull(clientSide, gotSend); err != nil {
		t.Fatal(err)
	}
	if string(gotSend) != wantSend {
		t.Fatalf("mux send output = %q, want %q", gotSend, wantSend)
	}
	if gotPayload := <-received; !bytes.Equal(gotPayload, payload) {
		t.Fatalf("ID 2 payload = %v, want %v", gotPayload, payload)
	}

	go func() { _, _ = peer4.conn.Write([]byte("FOUR")) }()
	readExact(t, clientSide, "+IPD,4,4:FOUR")
	_ = peer4.conn.Close()
	readExact(t, clientSide, "\r\n4,CLOSED\r\n")

	testExchange(t, clientSide, "AT+CIPCLOSE=2\r\n", "\r\n2,CLOSED\r\n\r\nOK\r\n")
	_ = peer2.conn.Close()
	testExchange(t, clientSide, "AT+CIPSTATUS\r\n", "\r\nSTATUS:2\r\n\r\nOK\r\n")
	testExchange(t, clientSide, "AT+CIPMUX=0\r\n", "\r\nOK\r\n")
	testExchange(t, clientSide, "AT+CIPMUX?\r\n", "\r\n+CIPMUX:0\r\n\r\nOK\r\n")

	cancel()
	_ = clientSide.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop after multiplexed exchange")
	}

	logText := logs.String()
	for _, fragment := range []string{"id=2", "id=4", "CIPSEND payload received", "socket received"} {
		if !strings.Contains(logText, fragment) {
			t.Fatalf("mux log is missing %q: %s", fragment, logText)
		}
	}
}

func TestEmulatorRejectsConnectionSyntaxForWrongMuxMode(t *testing.T) {
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
		done <- New(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Dialer: dialer}).Serve(ctx, emulatorSide)
	}()

	testExchange(t, clientSide, "ATE0\r\n", "ATE0\r\n\r\nOK\r\n")
	testExchange(t, clientSide, `AT+CIPSTART=0,"TCP","wrong.test",80`+"\r\n", "\r\nERROR\r\n")
	testExchange(t, clientSide, "AT+CIPSEND=0,1\r\n", "\r\nERROR\r\n")
	testExchange(t, clientSide, "AT+CIPCLOSE=0\r\n", "\r\nERROR\r\n")
	testExchange(t, clientSide, "AT+CIPMUX=1\r\n", "\r\nOK\r\n")
	testExchange(t, clientSide, `AT+CIPSTART="TCP","wrong.test",80`+"\r\n", "\r\nERROR\r\n")
	testExchange(t, clientSide, "AT+CIPSEND=1\r\n", "\r\nERROR\r\n")
	testExchange(t, clientSide, "AT+CIPCLOSE\r\n", "\r\nERROR\r\n")
	testExchange(t, clientSide, `AT+CIPSTART=0,"TCP","right.test",80`+"\r\n", "\r\n0,CONNECT\r\n\r\nOK\r\n")
	peer := <-serverConnections
	testExchange(t, clientSide, `AT+CIPSTART=0,"TCP","duplicate.test",80`+"\r\n", "\r\nERROR\r\n")
	testExchange(t, clientSide, "AT+CIPMUX=0\r\n", "\r\nERROR\r\n")
	testExchange(t, clientSide, "AT+CIPCLOSE=0\r\n", "\r\n0,CLOSED\r\n\r\nOK\r\n")
	_ = peer.Close()

	cancel()
	_ = clientSide.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop")
	}
}

func TestEmulatorResetClosesEveryMultiplexedSocket(t *testing.T) {
	serverConnections := make(chan net.Conn, 2)
	dialer := espDialerFunc(func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		serverConnections <- server
		return client, nil
	})
	emulatorSide, clientSide := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- New(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Dialer: dialer}).Serve(ctx, emulatorSide)
	}()

	testExchange(t, clientSide, "ATE0\r\n", "ATE0\r\n\r\nOK\r\n")
	testExchange(t, clientSide, "AT+CIPMUX=1\r\n", "\r\nOK\r\n")
	testExchange(t, clientSide, `AT+CIPSTART=0,"TCP","zero.test",80`+"\r\n", "\r\n0,CONNECT\r\n\r\nOK\r\n")
	peer0 := <-serverConnections
	testExchange(t, clientSide, `AT+CIPSTART=4,"TCP","four.test",80`+"\r\n", "\r\n4,CONNECT\r\n\r\nOK\r\n")
	peer4 := <-serverConnections

	testExchange(t, clientSide, "AT+RST\r\n", "\r\nOK\r\nWIFI CONNECTED\r\nWIFI GOT IP\r\n\r\nready\r\n")
	for id, peer := range map[int]net.Conn{0: peer0, 4: peer4} {
		buffer := make([]byte, 1)
		if _, err := peer.Read(buffer); !errors.Is(err, io.EOF) {
			t.Fatalf("peer %d read after reset = %v, want EOF", id, err)
		}
		_ = peer.Close()
	}
	testExchange(t, clientSide, "ATE0\r\n", "ATE0\r\n\r\nOK\r\n")
	testExchange(t, clientSide, "AT+CIPMUX?\r\n", "\r\n+CIPMUX:0\r\n\r\nOK\r\n")
	testExchange(t, clientSide, "AT+CIPSTATUS\r\n", "\r\nSTATUS:2\r\n\r\nOK\r\n")

	cancel()
	_ = clientSide.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop")
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

func readExact(t *testing.T, connection net.Conn, want string) {
	t.Helper()
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(connection, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("UART output = %q, want %q", got, want)
	}
}

type espDialerFunc func(context.Context, string, string) (net.Conn, error)

func (function espDialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return function(ctx, network, address)
}

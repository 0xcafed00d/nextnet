package sockets

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestManagerTCPRoundTripAndRemoteClose(t *testing.T) {
	server := make(chan net.Conn, 1)
	dialer := dialerFunc(func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" {
			t.Fatalf("network = %q, want tcp", network)
		}
		if address != "example.com:80" {
			t.Fatalf("address = %q, want example.com:80", address)
		}
		client, peer := net.Pipe()
		server <- peer
		return client, nil
	})
	data := make(chan []byte, 1)
	closed := make(chan struct{}, 1)
	manager := New(dialer, Events{
		Data:   func(payload []byte) { data <- payload },
		Closed: func() { closed <- struct{}{} },
	}, time.Second)

	if err := manager.StartTCP(context.Background(), "example.com", 80); err != nil {
		t.Fatal(err)
	}
	peer := <-server

	received := make(chan []byte, 1)
	go func() {
		payload := make([]byte, 5)
		_, _ = io.ReadFull(peer, payload)
		received <- payload
	}()
	if err := manager.Send([]byte("HELLO")); err != nil {
		t.Fatal(err)
	}
	if got := string(<-received); got != "HELLO" {
		t.Fatalf("server received %q", got)
	}

	go func() {
		_, _ = peer.Write([]byte{0x00, 0xff, 'A'})
		_ = peer.Close()
	}()
	select {
	case got := <-data:
		if string(got) != string([]byte{0x00, 0xff, 'A'}) {
			t.Fatalf("data event = %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for data event")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for remote close event")
	}
	if manager.Connected() {
		t.Fatal("manager remained connected after remote close")
	}
}

func TestManagerLocalCloseSuppressesEvent(t *testing.T) {
	server := make(chan net.Conn, 1)
	manager := New(dialerFunc(func(context.Context, string, string) (net.Conn, error) {
		client, peer := net.Pipe()
		server <- peer
		return client, nil
	}), Events{Closed: func() { t.Error("local close generated a remote close event") }}, time.Second)

	if err := manager.StartTCP(context.Background(), "localhost", 1); err != nil {
		t.Fatal(err)
	}
	peer := <-server
	defer peer.Close()
	if !manager.Close() {
		t.Fatal("Close reported no active connection")
	}
	if manager.Close() {
		t.Fatal("second Close reported an active connection")
	}
	if manager.Connected() {
		t.Fatal("manager remained connected after local close")
	}
	if err := manager.Send([]byte("x")); err != ErrNoConnection {
		t.Fatalf("Send error = %v, want ErrNoConnection", err)
	}
}

type dialerFunc func(context.Context, string, string) (net.Conn, error)

func (function dialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return function(ctx, network, address)
}

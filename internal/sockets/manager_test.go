package sockets

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

type dataEvent struct {
	id      int
	payload []byte
}

type peerConnection struct {
	address string
	conn    net.Conn
}

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
	data := make(chan dataEvent, 1)
	closed := make(chan int, 1)
	manager := New(dialer, Events{
		Data:   func(link Link, payload []byte) { data <- dataEvent{id: link.ID, payload: payload} },
		Closed: func(link Link) { closed <- link.ID },
	}, time.Second)

	if err := manager.StartTCP(context.Background(), Link{ID: 0}, "example.com", 80); err != nil {
		t.Fatal(err)
	}
	peer := <-server

	received := make(chan []byte, 1)
	go func() {
		payload := make([]byte, 5)
		_, _ = io.ReadFull(peer, payload)
		received <- payload
	}()
	if err := manager.Send(0, []byte("HELLO")); err != nil {
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
	case event := <-data:
		if event.id != 0 || string(event.payload) != string([]byte{0x00, 0xff, 'A'}) {
			t.Fatalf("data event = id %d payload %v", event.id, event.payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for data event")
	}
	select {
	case id := <-closed:
		if id != 0 {
			t.Fatalf("closed ID = %d, want 0", id)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for remote close event")
	}
	if manager.Connected(0) {
		t.Fatal("manager remained connected after remote close")
	}
	manager.Wait()
}

func TestManagerTracksMultipleSocketIDs(t *testing.T) {
	peers := make(chan peerConnection, 2)
	manager := New(dialerFunc(func(_ context.Context, _ string, address string) (net.Conn, error) {
		client, peer := net.Pipe()
		peers <- peerConnection{address: address, conn: peer}
		return client, nil
	}), Events{}, time.Second)

	if err := manager.StartTCP(context.Background(), Link{ID: 3, Multiplexed: true}, "three.test", 3003); err != nil {
		t.Fatal(err)
	}
	peer3 := <-peers
	if peer3.address != "three.test:3003" {
		t.Fatalf("first dial address = %q", peer3.address)
	}
	if err := manager.StartTCP(context.Background(), Link{ID: 1, Multiplexed: true}, "one.test", 1001); err != nil {
		t.Fatal(err)
	}
	peer1 := <-peers
	if peer1.address != "one.test:1001" {
		t.Fatalf("second dial address = %q", peer1.address)
	}

	if manager.Count() != 2 || !manager.Connected(1) || !manager.Connected(3) {
		t.Fatalf("connections were not tracked by ID")
	}
	statuses := manager.Statuses()
	if len(statuses) != 2 || statuses[0].ID != 1 || statuses[1].ID != 3 {
		t.Fatalf("statuses = %+v, want IDs 1 and 3 in order", statuses)
	}
	if statuses[0].RemoteHost != "one.test" || statuses[0].RemotePort != 1001 || statuses[0].Network != "TCP" {
		t.Fatalf("status 1 = %+v", statuses[0])
	}

	received := make(chan string, 1)
	go func() {
		payload := make([]byte, 5)
		_, _ = io.ReadFull(peer3.conn, payload)
		received <- string(payload)
	}()
	if err := manager.Send(3, []byte("THREE")); err != nil {
		t.Fatal(err)
	}
	if got := <-received; got != "THREE" {
		t.Fatalf("ID 3 peer received %q", got)
	}

	if !manager.Close(1) {
		t.Fatal("ID 1 was not closed")
	}
	_ = peer1.conn.Close()
	if manager.Connected(1) || !manager.Connected(3) || manager.Count() != 1 {
		t.Fatal("closing ID 1 affected the wrong connection")
	}
	if closed := manager.CloseAll(); closed != 1 {
		t.Fatalf("CloseAll closed %d connections, want 1", closed)
	}
	_ = peer3.conn.Close()
	if closed := manager.CloseAll(); closed != 0 {
		t.Fatalf("second CloseAll closed %d connections, want 0", closed)
	}
	manager.Wait()
}

func TestManagerRejectsDuplicateAndInvalidSocketIDs(t *testing.T) {
	server := make(chan net.Conn, 1)
	manager := New(dialerFunc(func(context.Context, string, string) (net.Conn, error) {
		client, peer := net.Pipe()
		server <- peer
		return client, nil
	}), Events{}, time.Second)

	for _, id := range []int{-1, 5} {
		if err := manager.StartTCP(context.Background(), Link{ID: id}, "localhost", 1); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("StartTCP ID %d error = %v, want ErrInvalidID", id, err)
		}
		if err := manager.Send(id, []byte("x")); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("Send ID %d error = %v, want ErrInvalidID", id, err)
		}
	}
	if err := manager.StartTCP(context.Background(), Link{ID: 2, Multiplexed: true}, "localhost", 1); err != nil {
		t.Fatal(err)
	}
	peer := <-server
	defer peer.Close()
	if err := manager.StartTCP(context.Background(), Link{ID: 2, Multiplexed: true}, "localhost", 1); !errors.Is(err, ErrAlreadyConnected) {
		t.Fatalf("duplicate StartTCP error = %v, want ErrAlreadyConnected", err)
	}
	manager.CloseAll()
	manager.Wait()
}

func TestManagerLocalCloseSuppressesEvent(t *testing.T) {
	server := make(chan net.Conn, 1)
	manager := New(dialerFunc(func(context.Context, string, string) (net.Conn, error) {
		client, peer := net.Pipe()
		server <- peer
		return client, nil
	}), Events{Closed: func(Link) { t.Error("local close generated a remote close event") }}, time.Second)

	if err := manager.StartTCP(context.Background(), Link{ID: 4, Multiplexed: true}, "localhost", 1); err != nil {
		t.Fatal(err)
	}
	peer := <-server
	defer peer.Close()
	if !manager.Close(4) {
		t.Fatal("Close reported no active connection")
	}
	if manager.Close(4) {
		t.Fatal("second Close reported an active connection")
	}
	if manager.Connected(4) {
		t.Fatal("manager remained connected after local close")
	}
	if err := manager.Send(4, []byte("x")); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("Send error = %v, want ErrNoConnection", err)
	}
	manager.Wait()
}

type dialerFunc func(context.Context, string, string) (net.Conn, error)

func (function dialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return function(ctx, network, address)
}

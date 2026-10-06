package sockets

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
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

func TestManagerAcceptsInboundTCPConnections(t *testing.T) {
	listener := newPipeListener(&net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 8080})
	connected := make(chan Link, 1)
	data := make(chan dataEvent, 1)
	closed := make(chan int, 1)
	manager := New(
		dialerFunc(func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("unexpected outgoing connection")
		}),
		Events{
			Connected: func(link Link) { connected <- link },
			Data:      func(link Link, payload []byte) { data <- dataEvent{id: link.ID, payload: payload} },
			Closed:    func(link Link) { closed <- link.ID },
		},
		time.Second,
		listenerFunc(func(_ context.Context, network, address string) (net.Listener, error) {
			if network != "tcp" || address != ":8080" {
				t.Fatalf("listen = %q %q, want tcp :8080", network, address)
			}
			return listener, nil
		}),
	)

	if err := manager.StartTCPServer(context.Background(), 8080); err != nil {
		t.Fatal(err)
	}
	if port, running := manager.ServerPort(); !running || port != 8080 {
		t.Fatalf("server = port %d running %v, want port 8080", port, running)
	}
	client, server := net.Pipe()
	listener.offer(&addressedConn{
		Conn:   server,
		local:  listener.Addr(),
		remote: &net.TCPAddr{IP: net.ParseIP("192.0.2.20"), Port: 4567},
	})
	defer client.Close()

	var link Link
	select {
	case link = <-connected:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inbound connection event")
	}
	if link.ID != 0 || !link.Inbound || !link.Multiplexed {
		t.Fatalf("inbound link = %+v", link)
	}
	statuses := manager.Statuses()
	if len(statuses) != 1 || statuses[0].ID != 0 || !statuses[0].Inbound || statuses[0].LocalPort != 8080 {
		t.Fatalf("inbound statuses = %+v", statuses)
	}

	if _, err := client.Write([]byte("REQUEST")); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-data:
		if event.id != 0 || string(event.payload) != "REQUEST" {
			t.Fatalf("inbound data = id %d payload %q", event.id, event.payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inbound data")
	}

	response := make(chan string, 1)
	go func() {
		buffer := make([]byte, 8)
		_, _ = io.ReadFull(client, buffer)
		response <- string(buffer)
	}()
	if err := manager.Send(0, []byte("RESPONSE")); err != nil {
		t.Fatal(err)
	}
	if got := <-response; got != "RESPONSE" {
		t.Fatalf("inbound client received %q", got)
	}

	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-closed:
		if id != 0 {
			t.Fatalf("closed ID = %d, want 0", id)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inbound close")
	}
	if !manager.StopTCPServer(false) {
		t.Fatal("server was not stopped")
	}
	manager.Wait()
}

func TestManagerAppliesTimeoutToExistingInboundConnection(t *testing.T) {
	listener := newPipeListener(&net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 8080})
	connected := make(chan Link, 1)
	timedOut := make(chan Link, 1)
	manager := New(nil, Events{
		Connected: func(link Link) { connected <- link },
		TimedOut:  func(link Link) { timedOut <- link },
	}, time.Second, listenerFunc(func(context.Context, string, string) (net.Listener, error) {
		return listener, nil
	}))
	if err := manager.StartTCPServer(context.Background(), 8080); err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	listener.offer(&addressedConn{
		Conn:   server,
		local:  listener.Addr(),
		remote: &net.TCPAddr{IP: net.ParseIP("192.0.2.20"), Port: 4567},
	})
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inbound connection")
	}

	manager.SetServerTimeout(25 * time.Millisecond)
	if got := manager.ServerTimeout(); got != 25*time.Millisecond {
		t.Fatalf("server timeout = %v, want 25ms", got)
	}
	select {
	case link := <-timedOut:
		if link.ID != 0 {
			t.Fatalf("timed out ID = %d, want 0", link.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("inbound connection did not time out")
	}
	buffer := make([]byte, 1)
	if _, err := client.Read(buffer); !errors.Is(err, io.EOF) {
		t.Fatalf("client read after timeout = %v, want EOF", err)
	}
	_ = client.Close()
	manager.Shutdown()
	manager.Wait()
}

func TestConfigureTCPKeepAlive(t *testing.T) {
	connection := &keepAliveConn{}
	if err := configureTCPKeepAlive(connection, Link{HasKeepAlive: true, KeepAlive: 7}); err != nil {
		t.Fatal(err)
	}
	if !connection.enabled || connection.period != 7*time.Second {
		t.Fatalf("enabled = %v period = %v", connection.enabled, connection.period)
	}
	if err := configureTCPKeepAlive(connection, Link{HasKeepAlive: true, KeepAlive: 0}); err != nil {
		t.Fatal(err)
	}
	if connection.enabled {
		t.Fatal("zero keepalive did not disable TCP keepalive")
	}
}

type keepAliveConn struct {
	net.Conn
	enabled bool
	period  time.Duration
}

func (connection *keepAliveConn) SetKeepAlive(enabled bool) error {
	connection.enabled = enabled
	return nil
}

func (connection *keepAliveConn) SetKeepAlivePeriod(period time.Duration) error {
	connection.period = period
	return nil
}

type dialerFunc func(context.Context, string, string) (net.Conn, error)

func (function dialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return function(ctx, network, address)
}

type listenerFunc func(context.Context, string, string) (net.Listener, error)

func (function listenerFunc) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	return function(ctx, network, address)
}

type pipeListener struct {
	connections chan net.Conn
	closed      chan struct{}
	closeOnce   sync.Once
	address     net.Addr
}

func newPipeListener(address net.Addr) *pipeListener {
	return &pipeListener{
		connections: make(chan net.Conn, 5),
		closed:      make(chan struct{}),
		address:     address,
	}
}

func (listener *pipeListener) offer(connection net.Conn) {
	listener.connections <- connection
}

func (listener *pipeListener) Accept() (net.Conn, error) {
	select {
	case <-listener.closed:
		return nil, net.ErrClosed
	default:
	}
	select {
	case connection := <-listener.connections:
		return connection, nil
	case <-listener.closed:
		return nil, net.ErrClosed
	}
}

func (listener *pipeListener) Close() error {
	listener.closeOnce.Do(func() { close(listener.closed) })
	return nil
}

func (listener *pipeListener) Addr() net.Addr { return listener.address }

type addressedConn struct {
	net.Conn
	local  net.Addr
	remote net.Addr
}

func (connection *addressedConn) LocalAddr() net.Addr  { return connection.local }
func (connection *addressedConn) RemoteAddr() net.Addr { return connection.remote }

package sockets

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	MinConnectionID = 0
	MaxConnectionID = 4
)

var (
	ErrAlreadyConnected = errors.New("socket ID is already connected")
	ErrNoConnection     = errors.New("socket ID is not connected")
	ErrInvalidID        = errors.New("socket ID is out of range")
)

type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

type Link struct {
	ID          int
	Multiplexed bool
}

type Events struct {
	Data   func(Link, []byte)
	Closed func(Link)
}

type Status struct {
	ID         int
	Network    string
	RemoteHost string
	RemotePort int
	LocalPort  int
}

type Manager struct {
	mu           sync.Mutex
	readers      sync.WaitGroup
	connections  map[int]*connection
	dialing      map[int]struct{}
	dialer       Dialer
	events       Events
	writeTimeout time.Duration
	readSize     int
}

type connection struct {
	net.Conn
	link       Link
	network    string
	remoteHost string
	remotePort int
	localPort  int
	localClose atomic.Bool
	writeMu    sync.Mutex
}

func New(dialer Dialer, events Events, writeTimeout time.Duration) *Manager {
	if events.Data == nil {
		events.Data = func(Link, []byte) {}
	}
	if events.Closed == nil {
		events.Closed = func(Link) {}
	}
	return &Manager{
		connections:  make(map[int]*connection),
		dialing:      make(map[int]struct{}),
		dialer:       dialer,
		events:       events,
		writeTimeout: writeTimeout,
		readSize:     4096,
	}
}

func (m *Manager) StartTCP(ctx context.Context, link Link, host string, port int) error {
	if err := validateID(link.ID); err != nil {
		return err
	}
	if host == "" {
		return fmt.Errorf("host must not be empty")
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("port %d is out of range", port)
	}

	m.mu.Lock()
	if _, connected := m.connections[link.ID]; connected {
		m.mu.Unlock()
		return fmt.Errorf("%w: %d", ErrAlreadyConnected, link.ID)
	}
	if _, dialing := m.dialing[link.ID]; dialing {
		m.mu.Unlock()
		return fmt.Errorf("%w: %d", ErrAlreadyConnected, link.ID)
	}
	m.dialing[link.ID] = struct{}{}
	m.mu.Unlock()

	address := net.JoinHostPort(host, strconv.Itoa(port))
	networkConnection, err := m.dialer.DialContext(ctx, "tcp", address)

	m.mu.Lock()
	delete(m.dialing, link.ID)
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("dial TCP %s: %w", address, err)
	}
	if ctx.Err() != nil {
		m.mu.Unlock()
		_ = networkConnection.Close()
		return ctx.Err()
	}
	if _, connected := m.connections[link.ID]; connected {
		m.mu.Unlock()
		_ = networkConnection.Close()
		return fmt.Errorf("%w: %d", ErrAlreadyConnected, link.ID)
	}
	active := &connection{
		Conn:       networkConnection,
		link:       link,
		network:    "TCP",
		remoteHost: remoteAddressHost(networkConnection.RemoteAddr(), host),
		remotePort: port,
		localPort:  addressPort(networkConnection.LocalAddr()),
	}
	m.connections[link.ID] = active
	m.readers.Add(1)
	m.mu.Unlock()

	go m.readLoop(active)
	return nil
}

func (m *Manager) Connected(id int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, connected := m.connections[id]
	return connected
}

func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.connections)
}

func (m *Manager) Statuses() []Status {
	m.mu.Lock()
	statuses := make([]Status, 0, len(m.connections))
	for _, active := range m.connections {
		statuses = append(statuses, Status{
			ID:         active.link.ID,
			Network:    active.network,
			RemoteHost: active.remoteHost,
			RemotePort: active.remotePort,
			LocalPort:  active.localPort,
		})
	}
	m.mu.Unlock()
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].ID < statuses[j].ID })
	return statuses
}

func (m *Manager) Send(id int, payload []byte) error {
	if err := validateID(id); err != nil {
		return err
	}
	m.mu.Lock()
	active := m.connections[id]
	m.mu.Unlock()
	if active == nil || active.localClose.Load() {
		return fmt.Errorf("%w: %d", ErrNoConnection, id)
	}

	active.writeMu.Lock()
	defer active.writeMu.Unlock()
	if active.localClose.Load() {
		return fmt.Errorf("%w: %d", ErrNoConnection, id)
	}

	if m.writeTimeout > 0 {
		if err := active.SetWriteDeadline(time.Now().Add(m.writeTimeout)); err != nil {
			return fmt.Errorf("set TCP write deadline: %w", err)
		}
		defer active.SetWriteDeadline(time.Time{})
	}
	if err := writeAll(active, payload); err != nil {
		_ = active.Conn.Close()
		return fmt.Errorf("write TCP payload: %w", err)
	}
	return nil
}

// Close closes one active socket without generating a remote CLOSED event. It
// reports whether the requested connection was present.
func (m *Manager) Close(id int) bool {
	m.mu.Lock()
	active := m.connections[id]
	if active != nil {
		delete(m.connections, id)
		active.localClose.Store(true)
	}
	m.mu.Unlock()
	if active == nil {
		return false
	}
	_ = active.Conn.Close()
	return true
}

// CloseAll closes every active socket without generating remote CLOSED events
// and returns the number of connections that were closed.
func (m *Manager) CloseAll() int {
	m.mu.Lock()
	activeConnections := make([]*connection, 0, len(m.connections))
	for id, active := range m.connections {
		delete(m.connections, id)
		active.localClose.Store(true)
		activeConnections = append(activeConnections, active)
	}
	m.mu.Unlock()
	for _, active := range activeConnections {
		_ = active.Conn.Close()
	}
	return len(activeConnections)
}

// Wait waits for all socket reader goroutines to finish. Call CloseAll first
// when shutting down. It is separate so command handlers never deadlock with a
// receive callback that is waiting to enqueue UART output.
func (m *Manager) Wait() {
	m.readers.Wait()
}

func (m *Manager) readLoop(active *connection) {
	defer m.readers.Done()
	buffer := make([]byte, m.readSize)
	for {
		n, err := active.Read(buffer)
		if n > 0 {
			payload := append([]byte(nil), buffer[:n]...)
			m.events.Data(active.link, payload)
		}
		if err != nil {
			if !active.localClose.Load() && m.remove(active) {
				m.events.Closed(active.link)
			}
			return
		}
	}
}

func (m *Manager) remove(active *connection) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.connections[active.link.ID] != active {
		return false
	}
	delete(m.connections, active.link.ID)
	return true
}

func validateID(id int) error {
	if id < MinConnectionID || id > MaxConnectionID {
		return fmt.Errorf("%w: %d (expected %d-%d)", ErrInvalidID, id, MinConnectionID, MaxConnectionID)
	}
	return nil
}

func remoteAddressHost(address net.Addr, fallback string) string {
	if address == nil {
		return fallback
	}
	if host, _, err := net.SplitHostPort(address.String()); err == nil && host != "" {
		return host
	}
	return fallback
}

func addressPort(address net.Addr) int {
	if address == nil {
		return 0
	}
	_, value, err := net.SplitHostPort(address.String())
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return port
}

func writeAll(writer io.Writer, payload []byte) error {
	for len(payload) > 0 {
		n, err := writer.Write(payload)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		payload = payload[n:]
	}
	return nil
}

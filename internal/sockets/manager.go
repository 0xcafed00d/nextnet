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
	ErrServerRunning    = errors.New("TCP server is already running")
)

type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

type Listener interface {
	Listen(ctx context.Context, network, address string) (net.Listener, error)
}

type Link struct {
	ID           int
	Multiplexed  bool
	Inbound      bool
	RemoteHost   string
	RemotePort   int
	KeepAlive    int
	HasKeepAlive bool
	generation   uint64
}

type Events struct {
	Connected      func(Link)
	Data           func(Link, []byte)
	Closed         func(Link)
	TimedOut       func(Link)
	ServerError    func(error)
	ServerRejected func(string)
}

type Status struct {
	ID         int
	Network    string
	RemoteHost string
	RemotePort int
	LocalPort  int
	Inbound    bool
}

type Manager struct {
	mu             sync.Mutex
	readers        sync.WaitGroup
	connections    map[int]*connection
	dialing        map[int]struct{}
	reserved       map[int]struct{}
	dialer         Dialer
	listener       Listener
	events         Events
	writeTimeout   time.Duration
	readSize       int
	server         *tcpServer
	startingServer bool
	nextGeneration uint64
	serverTimeout  time.Duration
}

type tcpServer struct {
	net.Listener
	port       int
	localClose atomic.Bool
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

func New(dialer Dialer, events Events, writeTimeout time.Duration, listeners ...Listener) *Manager {
	if events.Connected == nil {
		events.Connected = func(Link) {}
	}
	if events.Data == nil {
		events.Data = func(Link, []byte) {}
	}
	if events.Closed == nil {
		events.Closed = func(Link) {}
	}
	if events.TimedOut == nil {
		events.TimedOut = func(Link) {}
	}
	if events.ServerError == nil {
		events.ServerError = func(error) {}
	}
	if events.ServerRejected == nil {
		events.ServerRejected = func(string) {}
	}
	var listener Listener = &net.ListenConfig{}
	if len(listeners) > 0 && listeners[0] != nil {
		listener = listeners[0]
	}
	return &Manager{
		connections:  make(map[int]*connection),
		dialing:      make(map[int]struct{}),
		reserved:     make(map[int]struct{}),
		dialer:       dialer,
		listener:     listener,
		events:       events,
		writeTimeout: writeTimeout,
		readSize:     4096,
	}
}

func (m *Manager) StartTCPServer(ctx context.Context, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port %d is out of range", port)
	}

	m.mu.Lock()
	if m.server != nil || m.startingServer {
		m.mu.Unlock()
		return ErrServerRunning
	}
	m.startingServer = true
	m.mu.Unlock()

	address := net.JoinHostPort("", strconv.Itoa(port))
	listener, err := m.listener.Listen(ctx, "tcp", address)

	m.mu.Lock()
	m.startingServer = false
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("listen TCP %s: %w", address, err)
	}
	if ctx.Err() != nil {
		m.mu.Unlock()
		_ = listener.Close()
		return ctx.Err()
	}
	if m.server != nil {
		m.mu.Unlock()
		_ = listener.Close()
		return ErrServerRunning
	}
	server := &tcpServer{Listener: listener, port: port}
	m.server = server
	m.readers.Add(1)
	m.mu.Unlock()

	go m.acceptLoop(server)
	return nil
}

func (m *Manager) ServerPort() (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.server == nil {
		return 0, false
	}
	return m.server.port, true
}

// SetServerTimeout updates the inactivity deadline for current and future
// inbound clients. Outbound traffic does not refresh these deadlines.
func (m *Manager) SetServerTimeout(timeout time.Duration) {
	m.mu.Lock()
	m.serverTimeout = timeout
	inbound := make([]*connection, 0, len(m.connections))
	for _, active := range m.connections {
		if active.link.Inbound && !active.localClose.Load() {
			inbound = append(inbound, active)
		}
	}
	m.mu.Unlock()

	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for _, active := range inbound {
		_ = active.SetReadDeadline(deadline)
	}
}

func (m *Manager) ServerTimeout() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.serverTimeout
}

// StopTCPServer stops accepting new clients. When closeConnections is true,
// all client and server connections are closed, matching AT+CIPSERVER=0,1.
func (m *Manager) StopTCPServer(closeConnections bool) bool {
	m.mu.Lock()
	server := m.server
	if server != nil {
		m.server = nil
		server.localClose.Store(true)
	}
	m.mu.Unlock()
	if server != nil {
		_ = server.Close()
	}
	if closeConnections {
		m.CloseAll()
	}
	return server != nil
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
	if _, reserved := m.reserved[link.ID]; reserved {
		m.mu.Unlock()
		return fmt.Errorf("%w: %d", ErrAlreadyConnected, link.ID)
	}
	m.dialing[link.ID] = struct{}{}
	m.mu.Unlock()

	address := net.JoinHostPort(host, strconv.Itoa(port))
	networkConnection, err := m.dialer.DialContext(ctx, "tcp", address)
	if err == nil {
		if configureErr := configureTCPKeepAlive(networkConnection, link); configureErr != nil {
			_ = networkConnection.Close()
			err = configureErr
		}
	}

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
	m.nextGeneration++
	active.link.generation = m.nextGeneration
	active.link.RemoteHost = active.remoteHost
	active.link.RemotePort = active.remotePort
	m.connections[link.ID] = active
	m.readers.Add(1)
	m.mu.Unlock()

	go m.readLoop(active)
	return nil
}

type tcpKeepAliveConnection interface {
	SetKeepAlive(bool) error
	SetKeepAlivePeriod(time.Duration) error
}

func configureTCPKeepAlive(networkConnection net.Conn, link Link) error {
	if !link.HasKeepAlive {
		return nil
	}
	tcpConnection, ok := networkConnection.(tcpKeepAliveConnection)
	if !ok {
		return nil
	}
	enabled := link.KeepAlive > 0
	if err := tcpConnection.SetKeepAlive(enabled); err != nil {
		return fmt.Errorf("configure TCP keepalive: %w", err)
	}
	if enabled {
		if err := tcpConnection.SetKeepAlivePeriod(time.Duration(link.KeepAlive) * time.Second); err != nil {
			return fmt.Errorf("configure TCP keepalive period: %w", err)
		}
	}
	return nil
}

func (m *Manager) Connected(id int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, connected := m.connections[id]
	return connected
}

// Active reports whether link still names the same live connection. The
// generation check prevents delayed callbacks from being applied to a reused
// multiplexed ID.
func (m *Manager) Active(link Link) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	active := m.connections[link.ID]
	return active != nil && active.link.generation == link.generation
}

func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.connections)
}

// Hold keeps an ID unavailable after a remote close while passive receive data
// is still waiting for the UART peer to collect it.
func (m *Manager) Hold(id int) {
	if validateID(id) != nil {
		return
	}
	m.mu.Lock()
	m.reserved[id] = struct{}{}
	m.mu.Unlock()
}

func (m *Manager) Release(id int) {
	m.mu.Lock()
	delete(m.reserved, id)
	m.mu.Unlock()
}

func (m *Manager) InUse(id int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, connected := m.connections[id]
	_, reserved := m.reserved[id]
	return connected || reserved
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
			Inbound:    active.link.Inbound,
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
	_, reserved := m.reserved[id]
	delete(m.reserved, id)
	if active != nil {
		delete(m.connections, id)
		active.localClose.Store(true)
	}
	m.mu.Unlock()
	if active == nil {
		return reserved
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
	clear(m.reserved)
	m.mu.Unlock()
	for _, active := range activeConnections {
		_ = active.Conn.Close()
	}
	return len(activeConnections)
}

// Shutdown stops the listener and closes every connection.
func (m *Manager) Shutdown() {
	m.StopTCPServer(false)
	m.CloseAll()
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
			if active.link.Inbound {
				m.refreshServerDeadline(active)
			}
			payload := append([]byte(nil), buffer[:n]...)
			m.events.Data(active.link, payload)
		}
		if err != nil {
			if !active.localClose.Load() && m.remove(active) {
				if active.link.Inbound && isTimeout(err) {
					_ = active.Conn.Close()
					m.events.TimedOut(active.link)
				} else {
					m.events.Closed(active.link)
				}
			}
			return
		}
	}
}

func (m *Manager) refreshServerDeadline(active *connection) {
	m.mu.Lock()
	timeout := m.serverTimeout
	m.mu.Unlock()
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	_ = active.SetReadDeadline(deadline)
}

func isTimeout(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func (m *Manager) acceptLoop(server *tcpServer) {
	defer m.readers.Done()
	for {
		networkConnection, err := server.Accept()
		if err != nil {
			if !server.localClose.Load() {
				m.mu.Lock()
				if m.server == server {
					m.server = nil
				}
				m.mu.Unlock()
				m.events.ServerError(fmt.Errorf("accept TCP connection: %w", err))
			}
			return
		}

		remoteHost := remoteAddressHost(networkConnection.RemoteAddr(), "0.0.0.0")
		remotePort := addressPort(networkConnection.RemoteAddr())
		m.mu.Lock()
		id, available := m.availableIDLocked()
		if m.server != server || server.localClose.Load() || !available {
			m.mu.Unlock()
			_ = networkConnection.Close()
			if !available {
				m.events.ServerRejected(net.JoinHostPort(remoteHost, strconv.Itoa(remotePort)))
			}
			continue
		}
		link := Link{
			ID:          id,
			Multiplexed: true,
			Inbound:     true,
			RemoteHost:  remoteHost,
			RemotePort:  remotePort,
		}
		active := &connection{
			Conn:       networkConnection,
			link:       link,
			network:    "TCP",
			remoteHost: remoteHost,
			remotePort: remotePort,
			localPort:  server.port,
		}
		m.nextGeneration++
		active.link.generation = m.nextGeneration
		link = active.link
		m.connections[id] = active
		if m.serverTimeout > 0 {
			_ = active.SetReadDeadline(time.Now().Add(m.serverTimeout))
		}
		m.readers.Add(1)
		m.mu.Unlock()

		m.events.Connected(link)
		go m.readLoop(active)
	}
}

func (m *Manager) availableIDLocked() (int, bool) {
	for id := MinConnectionID; id <= MaxConnectionID; id++ {
		if _, connected := m.connections[id]; connected {
			continue
		}
		if _, dialing := m.dialing[id]; dialing {
			continue
		}
		if _, reserved := m.reserved[id]; reserved {
			continue
		}
		return id, true
	}
	return 0, false
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

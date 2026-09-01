package sockets

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrAlreadyConnected = errors.New("a socket is already connected")
	ErrNoConnection     = errors.New("no socket is connected")
)

type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

type Events struct {
	Data   func([]byte)
	Closed func()
}

type Manager struct {
	mu           sync.Mutex
	readers      sync.WaitGroup
	connection   *connection
	dialing      bool
	dialer       Dialer
	events       Events
	writeTimeout time.Duration
	readSize     int
}

type connection struct {
	net.Conn
	localClose atomic.Bool
	writeMu    sync.Mutex
}

func New(dialer Dialer, events Events, writeTimeout time.Duration) *Manager {
	if events.Data == nil {
		events.Data = func([]byte) {}
	}
	if events.Closed == nil {
		events.Closed = func() {}
	}
	return &Manager{
		dialer:       dialer,
		events:       events,
		writeTimeout: writeTimeout,
		readSize:     4096,
	}
}

func (m *Manager) StartTCP(ctx context.Context, host string, port int) error {
	if host == "" {
		return fmt.Errorf("host must not be empty")
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("port %d is out of range", port)
	}

	m.mu.Lock()
	if m.connection != nil || m.dialing {
		m.mu.Unlock()
		return ErrAlreadyConnected
	}
	m.dialing = true
	m.mu.Unlock()

	address := net.JoinHostPort(host, strconv.Itoa(port))
	networkConnection, err := m.dialer.DialContext(ctx, "tcp", address)

	m.mu.Lock()
	m.dialing = false
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("dial TCP %s: %w", address, err)
	}
	if ctx.Err() != nil {
		m.mu.Unlock()
		_ = networkConnection.Close()
		return ctx.Err()
	}
	if m.connection != nil {
		m.mu.Unlock()
		_ = networkConnection.Close()
		return ErrAlreadyConnected
	}
	active := &connection{Conn: networkConnection}
	m.connection = active
	m.readers.Add(1)
	m.mu.Unlock()

	go m.readLoop(active)
	return nil
}

func (m *Manager) Connected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connection != nil
}

func (m *Manager) Send(payload []byte) error {
	m.mu.Lock()
	active := m.connection
	m.mu.Unlock()
	if active == nil || active.localClose.Load() {
		return ErrNoConnection
	}

	active.writeMu.Lock()
	defer active.writeMu.Unlock()
	if active.localClose.Load() {
		return ErrNoConnection
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

// Close closes the active socket without generating a remote CLOSED event. It
// reports whether a connection was present.
func (m *Manager) Close() bool {
	m.mu.Lock()
	active := m.connection
	if active != nil {
		m.connection = nil
		active.localClose.Store(true)
	}
	m.mu.Unlock()
	if active == nil {
		return false
	}
	_ = active.Conn.Close()
	return true
}

// Wait waits for all socket reader goroutines to finish. Call Close first when
// shutting down. It is separate from Close so command handlers never deadlock
// with a receive callback that is waiting to enqueue UART output.
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
			m.events.Data(payload)
		}
		if err != nil {
			if !active.localClose.Load() && m.remove(active) {
				m.events.Closed()
			}
			return
		}
	}
}

func (m *Manager) remove(active *connection) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.connection != active {
		return false
	}
	m.connection = nil
	return true
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

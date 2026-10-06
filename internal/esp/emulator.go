package esp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"nextnet/internal/sockets"
)

const (
	maximumATLine       = 4096
	defaultMaxSendSize  = 64 * 1024
	outputFrames        = 64
	defaultDialTimeout  = 10 * time.Second
	defaultWriteTimeout = 10 * time.Second
	transparentGuard    = 20 * time.Millisecond
	passiveBufferSize   = 5760
	defaultStationSSID  = "MiSTer"
	virtualStationRSSI  = -30
	virtualWiFiChannel  = 1
)

type Transport interface {
	io.Reader
	io.Writer
	io.Closer
}

type outputFrame struct {
	data       []byte
	afterWrite func() error
	done       chan<- error
}

type Config struct {
	Version      string
	Baud         int
	SetBaud      func(int) error
	StationInfo  func() (StationInfo, error)
	Dialer       sockets.Dialer
	Listener     sockets.Listener
	DialTimeout  time.Duration
	WriteTimeout time.Duration
	MaxSendSize  int
}

type Emulator struct {
	logger                *slog.Logger
	config                Config
	defaultBaud           int
	currentBaud           int
	stationSSID           string
	echo                  bool
	mux                   bool
	cipdInfo              bool
	passiveReceive        bool
	serverTimeout         int
	transparentConfigured bool
	transparentActive     atomic.Bool
	transparentPluses     int
	transparentLastByte   time.Time
	transparentLastPlus   time.Time
	payloadExpected       int
	payloadSocketID       int
	payload               []byte
	outputMu              sync.Mutex
	passiveCond           *sync.Cond
	passiveData           [sockets.MaxConnectionID + 1][]byte
	passiveNotified       [sockets.MaxConnectionID + 1]bool
	passiveClosed         [sockets.MaxConnectionID + 1]bool
	passiveLinks          [sockets.MaxConnectionID + 1]sockets.Link
	socketManager         *sockets.Manager
}

func New(logger *slog.Logger, config Config) *Emulator {
	if config.Version == "" {
		config.Version = "dev"
	}
	if config.Baud <= 0 {
		config.Baud = 115200
	}
	if config.DialTimeout <= 0 {
		config.DialTimeout = defaultDialTimeout
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = defaultWriteTimeout
	}
	if config.MaxSendSize <= 0 {
		config.MaxSendSize = defaultMaxSendSize
	}
	if config.StationInfo == nil {
		config.StationInfo = discoverStationInfo
	}
	if config.Dialer == nil {
		config.Dialer = &net.Dialer{
			Timeout:   config.DialTimeout,
			KeepAlive: 30 * time.Second,
		}
	}
	emulator := &Emulator{
		logger:      logger,
		config:      config,
		defaultBaud: config.Baud,
		currentBaud: config.Baud,
		stationSSID: defaultStationSSID,
		echo:        true,
	}
	emulator.passiveCond = sync.NewCond(&emulator.outputMu)
	return emulator
}

// Serve processes AT commands until the transport closes or the context is
// canceled. Synchronous responses and asynchronous socket events all pass
// through one bounded queue and one writer goroutine.
func (e *Emulator) Serve(ctx context.Context, transport Transport) error {
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer transport.Close()

	output := make(chan outputFrame, outputFrames)
	e.socketManager = sockets.New(e.config.Dialer, sockets.Events{
		Connected: func(link sockets.Link) {
			e.emitConnected(serveCtx, output, link)
		},
		Data: func(link sockets.Link, payload []byte) {
			e.emitIPD(serveCtx, output, link, payload)
		},
		Closed: func(link sockets.Link) {
			e.emitClosed(serveCtx, output, link, "remote")
		},
		TimedOut: func(link sockets.Link) {
			e.emitClosed(serveCtx, output, link, "timeout")
		},
		ServerError: func(err error) {
			e.logger.Warn("TCP server stopped", "error", err)
		},
		ServerRejected: func(remote string) {
			e.logger.Warn("TCP server client rejected", "remote", remote, "reason", "no free connection ID")
		},
	}, e.config.WriteTimeout, e.config.Listener)

	writerDone := make(chan error, 1)
	go func() {
		err := writeLoop(serveCtx, transport, output)
		if err != nil {
			_ = transport.Close()
		}
		writerDone <- err
		cancel()
	}()

	closeOnCancelDone := make(chan struct{})
	go func() {
		select {
		case <-serveCtx.Done():
			e.socketManager.Shutdown()
			e.wakePassiveReceivers()
			_ = transport.Close()
		case <-closeOnCancelDone:
		}
	}()

	parser := newLineParser(maximumATLine)
	buffer := make([]byte, 1024)
	var readErr error

readLoop:
	for {
		n, err := transport.Read(buffer)
		if n > 0 {
			transparentPayload := make([]byte, 0, n)
			for _, value := range buffer[:n] {
				if parser.consumePendingLF(value) {
					continue
				}
				if e.transparentActive.Load() {
					consumed, escaped := e.consumeTransparentByte(value, &transparentPayload, time.Now())
					if escaped {
						e.sendTransparentPayload(transparentPayload)
						transparentPayload = transparentPayload[:0]
					}
					if consumed {
						continue
					}
				}
				if e.transparentPluses > 0 {
					// A remote close can leave an incomplete escape candidate.
					e.transparentPluses = 0
				}
				if e.payloadExpected > 0 {
					if processErr := e.processPayloadByte(serveCtx, output, value); processErr != nil {
						readErr = processErr
						break readLoop
					}
					continue
				}
				if event := parser.feedByte(value); event != nil {
					if processErr := e.processEvent(serveCtx, output, *event); processErr != nil {
						readErr = processErr
						break readLoop
					}
				}
			}
			e.sendTransparentPayload(transparentPayload)
		}
		if err != nil {
			readErr = err
			break
		}
	}

	cancel()
	e.socketManager.Shutdown()
	e.wakePassiveReceivers()
	_ = transport.Close()
	e.socketManager.Wait()
	close(output)
	writerErr := <-writerDone
	close(closeOnCancelDone)

	if ctx.Err() != nil {
		return nil
	}
	if writerErr != nil {
		return fmt.Errorf("UART write: %w", writerErr)
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return fmt.Errorf("UART read: %w", readErr)
	}
	return readErr
}

func (e *Emulator) processEvent(ctx context.Context, output chan<- outputFrame, event parseEvent) error {
	e.outputMu.Lock()
	defer e.outputMu.Unlock()

	if event.err != nil {
		e.logger.Warn("AT malformed", "error", event.err)
		if err := enqueue(ctx, output, responseError); err != nil {
			return err
		}
		e.logger.Info("AT -> ERROR")
		return nil
	}

	if !validCommandBytes(event.line) {
		e.logger.Warn("AT malformed", "command_bytes", strconv.QuoteToASCII(string(event.line)))
		if err := enqueue(ctx, output, responseError); err != nil {
			return err
		}
		e.logger.Info("AT -> ERROR")
		return nil
	}

	command := string(event.line)
	safeCommand := redactCommand(command)
	e.logger.Info("AT <- " + safeCommand)

	if e.echo {
		echo := append(append([]byte(nil), event.line...), '\r', '\n')
		if err := enqueue(ctx, output, echo); err != nil {
			return err
		}
	}

	result := e.executeCommand(ctx, command)
	switch {
	case !result.known:
		e.logger.Warn("AT unsupported", "command", safeCommand)
	case result.rejected != "":
		e.logger.Warn("AT rejected", "command", safeCommand, "reason", result.rejected)
	default:
		e.logger.Info("AT supported", "command", safeCommand)
	}
	if result.setBaud > 0 {
		oldBaud := e.currentBaud
		newBaud := result.setBaud
		if err := enqueueAfterWrite(ctx, output, result.response, func() error {
			return e.config.SetBaud(newBaud)
		}); err != nil {
			e.logger.Error("UART baud change failed", "from", oldBaud, "to", newBaud, "error", err)
			return err
		}
		e.currentBaud = newBaud
		e.logger.Info("UART baud changed", "from", oldBaud, "to", newBaud)
	} else if err := enqueue(ctx, output, result.response); err != nil {
		return err
	}
	e.logger.Info("AT -> " + result.responseLog)

	if result.reset {
		e.resetState()
	} else if result.setEcho != nil {
		e.echo = *result.setEcho
	}
	if result.enterTransparent {
		e.transparentPluses = 0
		e.transparentLastByte = time.Now()
		e.transparentLastPlus = time.Time{}
		e.transparentActive.Store(true)
		e.logger.Info("transparent mode entered", "id", 0)
	}
	if result.payloadLength > 0 {
		e.payloadExpected = result.payloadLength
		e.payloadSocketID = result.payloadConnectionID
		e.payload = make([]byte, 0, result.payloadLength)
	}
	return nil
}

// consumeTransparentByte collects raw UART bytes for socket ID 0. An ESP
// escape requires a quiet interval around three closely spaced plus bytes. A
// completed candidate is held until the next UART byte proves the trailing
// guard interval; that next byte is then returned to the AT line parser.
func (e *Emulator) consumeTransparentByte(value byte, payload *[]byte, now time.Time) (consumed, escaped bool) {
	if e.transparentPluses == 3 {
		if now.Sub(e.transparentLastPlus) > transparentGuard {
			e.transparentPluses = 0
			e.transparentActive.Store(false)
			e.logger.Info("transparent mode exited", "cause", "escape sequence")
			return false, true
		}
		e.appendTransparentPluses(payload)
		e.transparentLastByte = e.transparentLastPlus
	}

	if value != '+' {
		e.appendTransparentPluses(payload)
		*payload = append(*payload, value)
		e.transparentLastByte = now
		return true, false
	}

	if e.transparentPluses == 0 {
		if !e.transparentLastByte.IsZero() && now.Sub(e.transparentLastByte) <= transparentGuard {
			*payload = append(*payload, value)
			e.transparentLastByte = now
			return true, false
		}
		e.transparentPluses = 1
		e.transparentLastPlus = now
		return true, false
	}

	if now.Sub(e.transparentLastPlus) < transparentGuard {
		e.transparentPluses++
		e.transparentLastPlus = now
		return true, false
	}

	// A slow plus sequence is ordinary payload. The current plus begins a new
	// candidate because it follows the previous byte by a full guard interval.
	e.appendTransparentPluses(payload)
	e.transparentPluses = 1
	e.transparentLastPlus = now
	return true, false
}

func (e *Emulator) appendTransparentPluses(payload *[]byte) {
	for e.transparentPluses > 0 {
		*payload = append(*payload, '+')
		e.transparentPluses--
	}
}

func (e *Emulator) sendTransparentPayload(payload []byte) {
	if len(payload) == 0 {
		return
	}
	if err := e.socketManager.Send(0, payload); err != nil {
		e.logger.Warn("transparent socket send failed", "id", 0, "bytes", len(payload), "error", err)
		return
	}
	e.logger.Info("transparent socket sent", "id", 0, "bytes", len(payload))
}

func (e *Emulator) processPayloadByte(ctx context.Context, output chan<- outputFrame, value byte) error {
	e.payload = append(e.payload, value)
	if len(e.payload) < e.payloadExpected {
		return nil
	}

	payload := e.payload
	socketID := e.payloadSocketID
	e.payload = nil
	e.payloadExpected = 0
	e.payloadSocketID = 0
	e.logger.Info("CIPSEND payload received", "id", socketID, "bytes", len(payload))

	e.outputMu.Lock()
	defer e.outputMu.Unlock()
	if err := e.socketManager.Send(socketID, payload); err != nil {
		e.logger.Warn("socket send failed", "id", socketID, "bytes", len(payload), "error", err)
		if enqueueErr := enqueue(ctx, output, responseSendFail); enqueueErr != nil {
			return enqueueErr
		}
		e.logger.Info("AT -> SEND FAIL")
		return nil
	}
	if err := enqueue(ctx, output, responseSendOK); err != nil {
		return err
	}
	e.logger.Info("socket sent", "id", socketID, "bytes", len(payload))
	e.logger.Info("AT -> SEND OK")
	return nil
}

func (e *Emulator) emitIPD(ctx context.Context, output chan<- outputFrame, link sockets.Link, payload []byte) {
	e.outputMu.Lock()
	if e.socketManager != nil && !e.socketManager.Active(link) {
		e.outputMu.Unlock()
		return
	}
	// CIPMODE=1 selects passthrough receiving mode immediately. Bare CIPSEND
	// separately enables passthrough transmission from UART to the socket.
	transparent := e.transparentConfigured && link.ID == 0 && !link.Multiplexed
	for !transparent && e.passiveReceive && len(e.passiveData[link.ID]) > 0 &&
		len(e.passiveData[link.ID])+len(payload) > passiveBufferSize && ctx.Err() == nil {
		e.passiveCond.Wait()
	}
	if ctx.Err() != nil {
		e.outputMu.Unlock()
		return
	}
	if e.socketManager != nil && !e.socketManager.Active(link) {
		e.outputMu.Unlock()
		return
	}
	if !transparent && e.passiveReceive {
		e.socketManager.Hold(link.ID)
		e.passiveLinks[link.ID] = link
		e.passiveData[link.ID] = append(e.passiveData[link.ID], payload...)
		var err error
		if !e.passiveNotified[link.ID] {
			e.passiveNotified[link.ID] = true
			frame := passiveIPDFrame(link, len(e.passiveData[link.ID]))
			err = enqueue(ctx, output, frame)
		}
		e.outputMu.Unlock()
		if err == nil {
			e.logger.Info("socket received", "id", link.ID, "bytes", len(payload), "passive", true)
		}
		return
	}

	frame := append([]byte(nil), payload...)
	if !transparent {
		frame = make([]byte, 0, len(payload)+64)
		frame = append(frame, "+IPD,"...)
		if link.Multiplexed {
			frame = strconv.AppendInt(frame, int64(link.ID), 10)
			frame = append(frame, ',')
		}
		frame = strconv.AppendInt(frame, int64(len(payload)), 10)
		if e.cipdInfo {
			frame = append(frame, ',')
			frame = append(frame, link.RemoteHost...)
			frame = append(frame, ',')
			frame = strconv.AppendInt(frame, int64(link.RemotePort), 10)
		}
		frame = append(frame, ':')
		frame = append(frame, payload...)
	}

	err := enqueue(ctx, output, frame)
	e.outputMu.Unlock()
	if err == nil {
		e.logger.Info("socket received", "id", link.ID, "bytes", len(payload), "transparent", transparent, "passive", false)
	}
}

func passiveIPDFrame(link sockets.Link, length int) []byte {
	frame := make([]byte, 0, 32)
	frame = append(frame, "+IPD,"...)
	if link.Multiplexed {
		frame = strconv.AppendInt(frame, int64(link.ID), 10)
		frame = append(frame, ',')
	}
	frame = strconv.AppendInt(frame, int64(length), 10)
	frame = append(frame, '\r', '\n')
	return frame
}

func (e *Emulator) emitConnected(ctx context.Context, output chan<- outputFrame, link sockets.Link) {
	e.outputMu.Lock()
	if e.socketManager != nil && !e.socketManager.Active(link) {
		e.outputMu.Unlock()
		return
	}
	frame := connectionEvent(link.ID, "CONNECT", link.Multiplexed)
	err := enqueue(ctx, output, frame)
	e.outputMu.Unlock()
	if err == nil {
		e.logger.Info("server client connected",
			"id", link.ID,
			"network", "tcp",
			"host", link.RemoteHost,
			"port", link.RemotePort,
		)
	}
}

func (e *Emulator) emitClosed(ctx context.Context, output chan<- outputFrame, link sockets.Link, cause string) {
	e.outputMu.Lock()
	if len(e.passiveData[link.ID]) > 0 {
		buffered := len(e.passiveData[link.ID])
		e.passiveClosed[link.ID] = true
		e.socketManager.Hold(link.ID)
		e.outputMu.Unlock()
		e.logger.Info("socket close deferred", "id", link.ID, "cause", cause, "buffered", buffered)
		return
	}
	wasTransparent := e.transparentActive.Swap(false)
	frame := []byte("\r\nCLOSED\r\n")
	if link.Multiplexed {
		frame = []byte(fmt.Sprintf("\r\n%d,CLOSED\r\n", link.ID))
	}
	err := enqueue(ctx, output, frame)
	e.outputMu.Unlock()
	if err == nil {
		e.logger.Info("socket closed", "id", link.ID, "cause", cause, "transparent", wasTransparent)
	}
}

func (e *Emulator) resetState() {
	e.currentBaud = e.defaultBaud
	e.echo = true
	e.mux = false
	e.cipdInfo = false
	e.passiveReceive = false
	e.serverTimeout = 0
	if e.socketManager != nil {
		e.socketManager.SetServerTimeout(0)
	}
	e.transparentConfigured = false
	e.transparentActive.Store(false)
	e.transparentPluses = 0
	e.transparentLastByte = time.Time{}
	e.transparentLastPlus = time.Time{}
	e.payloadExpected = 0
	e.payloadSocketID = 0
	e.payload = nil
	e.clearAllPassiveData()
}

func (e *Emulator) clearPassiveData(id int) {
	e.passiveData[id] = nil
	e.passiveNotified[id] = false
	e.passiveClosed[id] = false
	e.passiveLinks[id] = sockets.Link{}
	if e.socketManager != nil {
		e.socketManager.Release(id)
	}
	e.passiveCond.Broadcast()
}

func (e *Emulator) clearAllPassiveData() {
	for id := sockets.MinConnectionID; id <= sockets.MaxConnectionID; id++ {
		e.clearPassiveData(id)
	}
}

func (e *Emulator) wakePassiveReceivers() {
	e.outputMu.Lock()
	e.passiveCond.Broadcast()
	e.outputMu.Unlock()
}

func enqueue(ctx context.Context, output chan<- outputFrame, data []byte) error {
	frame := outputFrame{data: append([]byte(nil), data...)}
	select {
	case output <- frame:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func enqueueAfterWrite(ctx context.Context, output chan<- outputFrame, data []byte, afterWrite func() error) error {
	done := make(chan error, 1)
	frame := outputFrame{
		data:       append([]byte(nil), data...),
		afterWrite: afterWrite,
		done:       done,
	}
	select {
	case output <- frame:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func writeLoop(ctx context.Context, transport io.Writer, output <-chan outputFrame) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case frame, ok := <-output:
			if !ok {
				return nil
			}
			if err := writeAll(transport, frame.data); err != nil {
				completeFrame(frame, err)
				return err
			}
			if frame.afterWrite != nil {
				err := frame.afterWrite()
				completeFrame(frame, err)
				if err != nil {
					return fmt.Errorf("post-write action: %w", err)
				}
			}
		}
	}
}

func completeFrame(frame outputFrame, err error) {
	if frame.done != nil {
		frame.done <- err
	}
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

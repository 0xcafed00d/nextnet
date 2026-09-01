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
	"time"

	"nextnet/internal/sockets"
)

const (
	maximumATLine       = 4096
	defaultMaxSendSize  = 64 * 1024
	outputFrames        = 64
	defaultDialTimeout  = 10 * time.Second
	defaultWriteTimeout = 10 * time.Second
)

type Transport interface {
	io.Reader
	io.Writer
	io.Closer
}

type Config struct {
	Version      string
	Baud         int
	Dialer       sockets.Dialer
	DialTimeout  time.Duration
	WriteTimeout time.Duration
	MaxSendSize  int
}

type Emulator struct {
	logger          *slog.Logger
	config          Config
	echo            bool
	mux             bool
	payloadExpected int
	payload         []byte
	outputMu        sync.Mutex
	socketManager   *sockets.Manager
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
	if config.Dialer == nil {
		config.Dialer = &net.Dialer{
			Timeout:   config.DialTimeout,
			KeepAlive: 30 * time.Second,
		}
	}
	return &Emulator{
		logger: logger,
		config: config,
		echo:   true,
	}
}

// Serve processes AT commands until the transport closes or the context is
// canceled. Synchronous responses and asynchronous socket events all pass
// through one bounded queue and one writer goroutine.
func (e *Emulator) Serve(ctx context.Context, transport Transport) error {
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer transport.Close()

	output := make(chan []byte, outputFrames)
	e.socketManager = sockets.New(e.config.Dialer, sockets.Events{
		Data: func(payload []byte) {
			e.emitIPD(serveCtx, output, payload)
		},
		Closed: func() {
			e.emitClosed(serveCtx, output)
		},
	}, e.config.WriteTimeout)

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
			e.socketManager.Close()
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
			for _, value := range buffer[:n] {
				if parser.consumePendingLF(value) {
					continue
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
		}
		if err != nil {
			readErr = err
			break
		}
	}

	cancel()
	e.socketManager.Close()
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

func (e *Emulator) processEvent(ctx context.Context, output chan<- []byte, event parseEvent) error {
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
	if err := enqueue(ctx, output, result.response); err != nil {
		return err
	}
	e.logger.Info("AT -> " + result.responseLog)

	if result.reset {
		e.resetState()
	} else if result.setEcho != nil {
		e.echo = *result.setEcho
	}
	if result.payloadLength > 0 {
		e.payloadExpected = result.payloadLength
		e.payload = make([]byte, 0, result.payloadLength)
	}
	return nil
}

func (e *Emulator) processPayloadByte(ctx context.Context, output chan<- []byte, value byte) error {
	e.payload = append(e.payload, value)
	if len(e.payload) < e.payloadExpected {
		return nil
	}

	payload := e.payload
	e.payload = nil
	e.payloadExpected = 0
	e.logger.Info("CIPSEND payload received", "bytes", len(payload))

	e.outputMu.Lock()
	defer e.outputMu.Unlock()
	if err := e.socketManager.Send(payload); err != nil {
		e.logger.Warn("socket send failed", "bytes", len(payload), "error", err)
		if enqueueErr := enqueue(ctx, output, responseSendFail); enqueueErr != nil {
			return enqueueErr
		}
		e.logger.Info("AT -> SEND FAIL")
		return nil
	}
	if err := enqueue(ctx, output, responseSendOK); err != nil {
		return err
	}
	e.logger.Info("socket sent", "id", 0, "bytes", len(payload))
	e.logger.Info("AT -> SEND OK")
	return nil
}

func (e *Emulator) emitIPD(ctx context.Context, output chan<- []byte, payload []byte) {
	frame := make([]byte, 0, len(payload)+32)
	frame = append(frame, "+IPD,"...)
	frame = strconv.AppendInt(frame, int64(len(payload)), 10)
	frame = append(frame, ':')
	frame = append(frame, payload...)

	e.outputMu.Lock()
	err := enqueue(ctx, output, frame)
	e.outputMu.Unlock()
	if err == nil {
		e.logger.Info("socket received", "id", 0, "bytes", len(payload))
	}
}

func (e *Emulator) emitClosed(ctx context.Context, output chan<- []byte) {
	e.outputMu.Lock()
	err := enqueue(ctx, output, []byte("\r\nCLOSED\r\n"))
	e.outputMu.Unlock()
	if err == nil {
		e.logger.Info("socket closed", "id", 0, "cause", "remote")
	}
}

func (e *Emulator) resetState() {
	e.echo = true
	e.mux = false
	e.payloadExpected = 0
	e.payload = nil
}

func enqueue(ctx context.Context, output chan<- []byte, frame []byte) error {
	copyOfFrame := append([]byte(nil), frame...)
	select {
	case output <- copyOfFrame:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func writeLoop(ctx context.Context, transport io.Writer, output <-chan []byte) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case frame, ok := <-output:
			if !ok {
				return nil
			}
			if err := writeAll(transport, frame); err != nil {
				return err
			}
		}
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

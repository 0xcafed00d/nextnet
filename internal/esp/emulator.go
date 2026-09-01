package esp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
)

const (
	maximumATLine = 4096
	outputFrames  = 64
)

type Transport interface {
	io.Reader
	io.Writer
	io.Closer
}

type Emulator struct {
	logger  *slog.Logger
	version string
	echo    bool
}

func New(logger *slog.Logger, emulatorVersion string) *Emulator {
	return &Emulator{
		logger:  logger,
		version: emulatorVersion,
		echo:    true,
	}
}

// Serve processes AT commands until the transport closes or the context is
// canceled. Every UART write passes through one bounded queue and one writer
// goroutine so future asynchronous socket frames cannot interleave responses.
func (e *Emulator) Serve(ctx context.Context, transport Transport) error {
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer transport.Close()

	output := make(chan []byte, outputFrames)
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
			for _, event := range parser.feed(buffer[:n]) {
				if processErr := e.processEvent(serveCtx, output, event); processErr != nil {
					readErr = processErr
					break readLoop
				}
			}
		}
		if err != nil {
			readErr = err
			break
		}
	}

	cancel()
	_ = transport.Close()
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

	echoWasEnabled := e.echo
	result := executeCommand(command, e.version)
	if echoWasEnabled {
		echo := append(append([]byte(nil), event.line...), '\r', '\n')
		if err := enqueue(ctx, output, echo); err != nil {
			return err
		}
	}

	if result.supported {
		e.logger.Info("AT supported", "command", safeCommand)
	} else {
		e.logger.Warn("AT unsupported", "command", safeCommand)
	}
	if err := enqueue(ctx, output, result.response); err != nil {
		return err
	}
	e.logger.Info("AT -> " + result.responseLog)

	if result.reset {
		e.echo = true
	} else if result.setEcho != nil {
		e.echo = *result.setEcho
	}
	return nil
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

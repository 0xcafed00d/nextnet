package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"nextnet/internal/corewatch"
	"nextnet/internal/esp"
	"nextnet/internal/serial"
	"nextnet/internal/version"
)

type Config struct {
	Device        string
	Baud          int
	Core          string
	CoreFile      string
	PollInterval  time.Duration
	RetryInterval time.Duration
}

type App struct {
	config  Config
	logger  *slog.Logger
	resolve func(string) (string, error)
	open    func(string, int) (serial.Transport, error)
}

type session struct {
	device string
	port   serial.Transport
	cancel context.CancelFunc
	done   <-chan error
}

func New(config Config, logger *slog.Logger) *App {
	return &App{
		config:  config,
		logger:  logger,
		resolve: serial.Resolve,
		open:    serial.Open,
	}
}

// Run maintains exactly one UART session while CORENAME exactly matches the
// configured core. Every other value, including alternative launcher names,
// missing files, and empty files, closes the session and leaves the app idle.
func (a *App) Run(ctx context.Context) error {
	if err := a.validate(); err != nil {
		return err
	}

	ticker := time.NewTicker(a.config.PollInterval)
	defer ticker.Stop()

	var (
		activeSession *session
		sessionDone   <-chan error
		lastCore      string
		haveLastCore  bool
		lastReadOK    = true
		retryAt       time.Time
	)

	for {
		active, coreName, readErr := corewatch.Active(a.config.CoreFile, a.config.Core)
		if readErr != nil {
			active = false
			coreName = ""
			if lastReadOK {
				a.logger.Warn("core file unavailable; bridge inactive",
					"path", a.config.CoreFile,
					"error", readErr,
				)
			}
			lastReadOK = false
		} else {
			if !lastReadOK {
				a.logger.Info("core file available", "path", a.config.CoreFile)
			}
			lastReadOK = true
		}

		if !haveLastCore || coreName != lastCore {
			a.logger.Info("core changed", "from", displayCore(lastCore, haveLastCore), "to", displayCore(coreName, true))
			lastCore = coreName
			haveLastCore = true
		}

		if !active && activeSession != nil {
			a.stopSession(activeSession)
			a.logger.Info("serial closed; bridge idle", "device", activeSession.device)
			activeSession = nil
			sessionDone = nil
			retryAt = time.Time{}
		}

		if active && activeSession == nil && !time.Now().Before(retryAt) {
			started, err := a.startSession(ctx)
			if err != nil {
				a.logger.Error("serial start failed; will retry while core remains active", "error", err)
				retryAt = time.Now().Add(a.config.RetryInterval)
			} else {
				activeSession = started
				sessionDone = started.done
				retryAt = time.Time{}
			}
		}

		select {
		case <-ctx.Done():
			if activeSession != nil {
				a.stopSession(activeSession)
				a.logger.Info("serial closed", "device", activeSession.device)
			}
			return nil
		case err := <-sessionDone:
			if activeSession != nil {
				_ = activeSession.port.Close()
				activeSession.cancel()
				if err != nil && !errors.Is(err, io.EOF) {
					a.logger.Error("serial session ended", "device", activeSession.device, "error", err)
				} else {
					a.logger.Warn("serial session ended", "device", activeSession.device)
				}
			}
			activeSession = nil
			sessionDone = nil
			retryAt = time.Now().Add(a.config.RetryInterval)
		case <-ticker.C:
		}
	}
}

func (a *App) startSession(parent context.Context) (*session, error) {
	device, err := a.resolve(a.config.Device)
	if err != nil {
		return nil, err
	}
	port, err := a.open(device, a.config.Baud)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(parent)
	done := make(chan error, 1)
	emulator := esp.New(a.logger, esp.Config{
		Version: version.String(),
		Baud:    a.config.Baud,
	})
	go func() {
		done <- emulator.Serve(ctx, port)
	}()

	a.logger.Info("serial opened",
		"device", device,
		"baud", a.config.Baud,
		"format", "8N1",
	)
	a.logger.Info("ESP emulator active")
	return &session{device: device, port: port, cancel: cancel, done: done}, nil
}

func (a *App) stopSession(active *session) {
	active.cancel()
	_ = active.port.Close()
	select {
	case <-active.done:
	case <-time.After(2 * time.Second):
		a.logger.Warn("timed out waiting for serial session shutdown", "device", active.device)
	}
}

func (a *App) validate() error {
	if a.config.Core == "" {
		return fmt.Errorf("core name must not be empty")
	}
	if a.config.CoreFile == "" {
		return fmt.Errorf("core file path must not be empty")
	}
	if a.config.Baud <= 0 {
		return fmt.Errorf("baud rate must be positive")
	}
	if a.config.PollInterval <= 0 {
		return fmt.Errorf("poll interval must be positive")
	}
	if a.config.RetryInterval <= 0 {
		return fmt.Errorf("retry interval must be positive")
	}
	return nil
}

func displayCore(name string, known bool) string {
	if !known {
		return "<startup>"
	}
	if name == "" {
		return "<none>"
	}
	return name
}

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"nextnet/internal/app"
	"nextnet/internal/version"
)

func main() {
	var (
		device   = flag.String("device", "", "serial device (default: auto-detect /dev/ttyS1)")
		baud     = flag.Int("baud", 115200, "UART baud rate")
		core     = flag.String("core", "ZXNext", "exact MiSTer core name that activates the bridge")
		coreFile = flag.String("core-file", "/tmp/CORENAME", "file containing the active MiSTer core name")
		logLevel = flag.String("log-level", "info", "debug, info, warn, or error")
		showVer  = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println(version.String())
		return
	}

	level, err := parseLogLevel(*logLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nextnet: %v\n", err)
		os.Exit(2)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("nextnet starting",
		"version", version.String(),
		"core", *core,
		"core_file", *coreFile,
		"device", displayDevice(*device),
		"baud", *baud,
	)

	runner := app.New(app.Config{
		Device:        *device,
		Baud:          *baud,
		Core:          *core,
		CoreFile:      *coreFile,
		PollInterval:  300 * time.Millisecond,
		RetryInterval: time.Second,
	}, logger)

	if err := runner.Run(ctx); err != nil {
		logger.Error("nextnet stopped", "error", err)
		os.Exit(1)
	}
	logger.Info("nextnet stopped")
}

func parseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid -log-level %q (want debug, info, warn, or error)", value)
	}
}

func displayDevice(device string) string {
	if device == "" {
		return "auto"
	}
	return device
}

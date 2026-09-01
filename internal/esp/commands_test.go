package esp

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

func TestRedactCommand(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{input: `AT+CWJAP="network","secret"`, want: "AT+CWJAP=<redacted>"},
		{input: `at+cwjap_cur="network","secret"`, want: "at+cwjap_cur=<redacted>"},
		{input: "AT+CWJAP?", want: "AT+CWJAP?"},
		{input: "AT", want: "AT"},
	} {
		if got := redactCommand(tc.input); got != tc.want {
			t.Errorf("redactCommand(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestExecuteBasicCommands(t *testing.T) {
	emulator := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Version: "test", Baud: 115200})
	for _, command := range []string{"AT", "ATE0", "ATE1", "AT+GMR", "AT+RST"} {
		result := emulator.executeCommand(context.Background(), command)
		if !result.known || result.rejected != "" {
			t.Errorf("%s result = known %v rejected %q", command, result.known, result.rejected)
		}
	}
	if result := emulator.executeCommand(context.Background(), "AT+NOTREAL"); result.known {
		t.Fatal("unknown command was supported")
	}
}

func TestUARTCompatibilityCommands(t *testing.T) {
	emulator := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Baud: 115200})
	for _, command := range []string{
		"AT+UART_CUR?",
		"AT+UART_DEF?",
		"AT+UART?",
		"AT+UART_CUR=115200,8,1,0,0",
		"AT+UART_DEF=115200,8,1,0,0",
		"AT+UART=115200,8,1,0,0",
	} {
		result := emulator.executeCommand(context.Background(), command)
		if !result.known || result.rejected != "" {
			t.Errorf("%s result = known %v rejected %q", command, result.known, result.rejected)
		}
	}
	result := emulator.executeCommand(context.Background(), "AT+UART_CUR=9600,8,1,0,0")
	if !result.known || result.rejected == "" {
		t.Fatalf("mismatched fixed baud was not rejected: %+v", result)
	}
}

func TestParseCIPStart(t *testing.T) {
	host, port, err := parseCIPStart(`AT+CIPSTART="TCP","example.com",8080`)
	if err != nil {
		t.Fatal(err)
	}
	if host != "example.com" || port != 8080 {
		t.Fatalf("got host=%q port=%d", host, port)
	}
	for _, command := range []string{
		`AT+CIPSTART="UDP","example.com",80`,
		`AT+CIPSTART="TCP","example.com",0`,
		`AT+CIPSTART=0,"TCP","example.com",80`,
	} {
		if _, _, err := parseCIPStart(command); err == nil {
			t.Errorf("parseCIPStart(%q) succeeded", command)
		}
	}
}

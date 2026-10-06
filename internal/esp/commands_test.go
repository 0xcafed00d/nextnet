package esp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
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
		t.Fatalf("baud change without a capable transport was not rejected: %+v", result)
	}

	dynamic := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
		Baud:    115200,
		SetBaud: func(int) error { return nil },
	})
	result = dynamic.executeCommand(context.Background(), "AT+UART_CUR=230769,8,1,0,0")
	if !result.known || result.rejected != "" || result.setBaud != 230769 {
		t.Fatalf("non-standard baud change was not accepted: %+v", result)
	}
	for _, command := range []string{
		"AT+UART_CUR=230769,7,1,0,0",
		"AT+UART_CUR=5000001,8,1,0,0",
		"AT+UART_DEF=230769,8,1,0,0",
	} {
		result = dynamic.executeCommand(context.Background(), command)
		if !result.known || result.rejected == "" {
			t.Fatalf("unsupported UART configuration %q was not rejected: %+v", command, result)
		}
	}
}

func TestWiFiStationCompatibilityCommands(t *testing.T) {
	emulator := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
		StationInfo: func() (StationInfo, error) {
			return StationInfo{
				IPAddress:  "192.0.2.10",
				MACAddress: "02:00:00:00:00:01",
			}, nil
		},
	})

	for _, command := range []string{
		"AT+CWMODE=1",
		"AT+CWMODE_CUR=1",
		"AT+CWMODE_DEF=1",
	} {
		result := emulator.executeCommand(context.Background(), command)
		if !result.known || result.rejected != "" || string(result.response) != string(responseOK) {
			t.Errorf("%s result = %+v", command, result)
		}
	}

	for _, tc := range []struct {
		command string
		want    string
	}{
		{command: "AT+CWMODE?", want: "\r\n+CWMODE:1\r\n\r\nOK\r\n"},
		{command: "AT+CWMODE_CUR?", want: "\r\n+CWMODE_CUR:1\r\n\r\nOK\r\n"},
		{command: "AT+CWMODE_DEF?", want: "\r\n+CWMODE_DEF:1\r\n\r\nOK\r\n"},
	} {
		result := emulator.executeCommand(context.Background(), tc.command)
		if !result.known || result.rejected != "" || string(result.response) != tc.want {
			t.Errorf("%s response = %q, rejected %q", tc.command, result.response, result.rejected)
		}
	}

	for _, command := range []string{
		"AT+CWMODE=0",
		"AT+CWMODE=2",
		"AT+CWMODE=3",
		"AT+CWMODE=station",
	} {
		result := emulator.executeCommand(context.Background(), command)
		if !result.known || result.rejected == "" || string(result.response) != string(responseError) {
			t.Errorf("unsupported Wi-Fi mode %s was not rejected: %+v", command, result)
		}
	}

	result := emulator.executeCommand(context.Background(), "AT+CWLAP")
	want := "\r\n+CWLAP:(0,\"MiSTer\",-30,\"02:00:00:00:00:01\",1)\r\n\r\nOK\r\n"
	if !result.known || result.rejected != "" || string(result.response) != want {
		t.Fatalf("AT+CWLAP response = %q, rejected %q", result.response, result.rejected)
	}

	for _, tc := range []struct {
		command string
		label   string
	}{
		{command: "AT+CWJAP?", label: "+CWJAP"},
		{command: "AT+CWJAP_CUR?", label: "+CWJAP_CUR"},
		{command: "AT+CWJAP_DEF?", label: "+CWJAP_DEF"},
	} {
		result = emulator.executeCommand(context.Background(), tc.command)
		want = fmt.Sprintf("\r\n%s:\"MiSTer\",\"02:00:00:00:00:01\",1,-30\r\n\r\nOK\r\n", tc.label)
		if !result.known || result.rejected != "" || string(result.response) != want {
			t.Errorf("%s response = %q, rejected %q", tc.command, result.response, result.rejected)
		}
	}

	result = emulator.executeCommand(context.Background(), `AT+CWJAP_CUR="Home Network","super-secret"`)
	want = "\r\nWIFI CONNECTED\r\nWIFI GOT IP\r\n\r\nOK\r\n"
	if !result.known || result.rejected != "" || string(result.response) != want {
		t.Fatalf("AT+CWJAP_CUR response = %q, rejected %q", result.response, result.rejected)
	}
	if strings.Contains(string(result.response), "super-secret") {
		t.Fatal("CWJAP response exposed the supplied password")
	}
	result = emulator.executeCommand(context.Background(), "AT+CWJAP?")
	if !strings.Contains(string(result.response), `+CWJAP:"Home Network"`) {
		t.Fatalf("CWJAP query did not retain virtual SSID: %q", result.response)
	}

	for _, command := range []string{`AT+CWJAP=""`, `AT+CWJAP="name"`} {
		result = emulator.executeCommand(context.Background(), command)
		if !result.known || result.rejected == "" || string(result.response) != string(responseError) {
			t.Errorf("invalid join command %q was not rejected: %+v", command, result)
		}
	}

	result = emulator.executeCommand(context.Background(), "AT+CIFSR")
	want = "\r\n+CIFSR:STAIP,\"192.0.2.10\"\r\n+CIFSR:STAMAC,\"02:00:00:00:00:01\"\r\n\r\nOK\r\n"
	if !result.known || result.rejected != "" || string(result.response) != want {
		t.Fatalf("AT+CIFSR response = %q, rejected %q", result.response, result.rejected)
	}
}

func TestCIFSRFallsBackWhenInterfaceDiscoveryFails(t *testing.T) {
	emulator := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
		StationInfo: func() (StationInfo, error) {
			return StationInfo{}, errors.New("no interface")
		},
	})

	result := emulator.executeCommand(context.Background(), "AT+CIFSR")
	want := "\r\n+CIFSR:STAIP,\"0.0.0.0\"\r\n+CIFSR:STAMAC,\"00:00:00:00:00:00\"\r\n\r\nOK\r\n"
	if !result.known || result.rejected != "" || string(result.response) != want {
		t.Fatalf("AT+CIFSR fallback = %q, rejected %q", result.response, result.rejected)
	}
}

func TestTransmissionCompatibilityModes(t *testing.T) {
	emulator := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{})
	for _, command := range []string{
		"AT+CIPDINFO=0",
		"AT+CIPDINFO=1",
		"AT+CIPRECVMODE=0",
		"AT+CIPRECVMODE=1",
		"AT+CIPMODE=0",
		"AT+CIPMODE=1",
	} {
		result := emulator.executeCommand(context.Background(), command)
		if !result.known || result.rejected != "" {
			t.Errorf("%s result = known %v rejected %q", command, result.known, result.rejected)
		}
	}
	for _, command := range []string{"AT+CIPDINFO?", "AT+CIPRECVMODE?", "AT+CIPMODE?"} {
		result := emulator.executeCommand(context.Background(), command)
		if !result.known || result.rejected != "" || !strings.Contains(string(result.response), ":1") {
			t.Errorf("%s result = %+v", command, result)
		}
	}
	for _, command := range []string{"AT+CIPDINFO=2", "AT+CIPRECVMODE=-1", "AT+CIPMODE=YES"} {
		result := emulator.executeCommand(context.Background(), command)
		if !result.known || result.rejected == "" {
			t.Errorf("invalid mode command %s was not rejected: %+v", command, result)
		}
	}

	emulator.mux = true
	result := emulator.executeCommand(context.Background(), "AT+CIPMODE=1")
	if !result.known || result.rejected == "" {
		t.Fatalf("transparent mode with CIPMUX=1 was not rejected: %+v", result)
	}
}

func TestCIPServerTimeoutCommands(t *testing.T) {
	emulator := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{})

	result := emulator.executeCommand(context.Background(), "AT+CIPSTO?")
	if !result.known || result.rejected != "" || string(result.response) != "\r\n+CIPSTO:0\r\n\r\nOK\r\n" {
		t.Fatalf("initial CIPSTO query = %+v", result)
	}
	result = emulator.executeCommand(context.Background(), "AT+CIPSTO=30")
	if !result.known || result.rejected != "" || string(result.response) != string(responseOK) || emulator.serverTimeout != 30 {
		t.Fatalf("CIPSTO setter = %+v timeout %d", result, emulator.serverTimeout)
	}
	result = emulator.executeCommand(context.Background(), "AT+CIPSTO?")
	if !result.known || result.rejected != "" || string(result.response) != "\r\n+CIPSTO:30\r\n\r\nOK\r\n" {
		t.Fatalf("updated CIPSTO query = %+v", result)
	}
	for _, command := range []string{"AT+CIPSTO=-1", "AT+CIPSTO=7201", "AT+CIPSTO=thirty"} {
		result = emulator.executeCommand(context.Background(), command)
		if !result.known || result.rejected == "" || string(result.response) != string(responseError) {
			t.Errorf("invalid timeout %q was not rejected: %+v", command, result)
		}
	}
}

func TestParseCIPStart(t *testing.T) {
	arguments, err := parseCIPStart(`AT+CIPSTART="TCP","example.com",8080`)
	if err != nil {
		t.Fatal(err)
	}
	if arguments.hasID || arguments.id != 0 || arguments.host != "example.com" || arguments.port != 8080 {
		t.Fatalf("single arguments = %+v", arguments)
	}
	arguments, err = parseCIPStart(`AT+CIPSTART=4,"TCP","mux.example",443`)
	if err != nil {
		t.Fatal(err)
	}
	if !arguments.hasID || arguments.id != 4 || arguments.host != "mux.example" || arguments.port != 443 {
		t.Fatalf("multiplexed arguments = %+v", arguments)
	}
	arguments, err = parseCIPStart(`AT+CIPSTART="TCP","bbs.zxnext.uk",2323,1`)
	if err != nil {
		t.Fatal(err)
	}
	if arguments.hasID || !arguments.hasKeepAlive || arguments.keepAlive != 1 || arguments.host != "bbs.zxnext.uk" || arguments.port != 2323 {
		t.Fatalf("single keepalive arguments = %+v", arguments)
	}
	arguments, err = parseCIPStart(`AT+CIPSTART=2,"TCP","mux.example",443,60`)
	if err != nil {
		t.Fatal(err)
	}
	if !arguments.hasID || arguments.id != 2 || !arguments.hasKeepAlive || arguments.keepAlive != 60 {
		t.Fatalf("multiplexed keepalive arguments = %+v", arguments)
	}
	for _, command := range []string{
		`AT+CIPSTART="UDP","example.com",80`,
		`AT+CIPSTART="TCP","example.com",0`,
		`AT+CIPSTART="TCP","example.com",80,-1`,
		`AT+CIPSTART="TCP","example.com",80,7201`,
		`AT+CIPSTART=5,"TCP","example.com",80`,
		`AT+CIPSTART=0,1,"TCP","example.com",80`,
	} {
		if _, err := parseCIPStart(command); err == nil {
			t.Errorf("parseCIPStart(%q) succeeded", command)
		}
	}
}

func TestParseCIPSend(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    cipSendArguments
	}{
		{command: "AT+CIPSEND=12", want: cipSendArguments{id: 0, length: 12}},
		{command: "AT+CIPSEND=3,4096", want: cipSendArguments{id: 3, hasID: true, length: 4096}},
	} {
		got, err := parseCIPSend(tc.command)
		if err != nil {
			t.Fatalf("parseCIPSend(%q): %v", tc.command, err)
		}
		if got != tc.want {
			t.Fatalf("parseCIPSend(%q) = %+v, want %+v", tc.command, got, tc.want)
		}
	}
	for _, command := range []string{
		"AT+CIPSEND=0",
		"AT+CIPSEND=-1",
		"AT+CIPSEND=5,10",
		"AT+CIPSEND=0,1,2",
	} {
		if _, err := parseCIPSend(command); err == nil {
			t.Errorf("parseCIPSend(%q) succeeded", command)
		}
	}
}

func TestParseCIPServer(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    cipServerArguments
	}{
		{command: "AT+CIPSERVER=0", want: cipServerArguments{mode: 0, port: 333}},
		{command: "AT+CIPSERVER=0,1", want: cipServerArguments{mode: 0, port: 333, closeConnections: true}},
		{command: "AT+CIPSERVER=1", want: cipServerArguments{mode: 1, port: 333}},
		{command: "AT+CIPSERVER=1,80", want: cipServerArguments{mode: 1, port: 80}},
		{command: `AT+CIPSERVER=1,8080,"TCP"`, want: cipServerArguments{mode: 1, port: 8080}},
	} {
		got, err := parseCIPServer(tc.command)
		if err != nil {
			t.Fatalf("parseCIPServer(%q): %v", tc.command, err)
		}
		if got != tc.want {
			t.Fatalf("parseCIPServer(%q) = %+v, want %+v", tc.command, got, tc.want)
		}
	}
	for _, command := range []string{
		"AT+CIPSERVER=2",
		"AT+CIPSERVER=0,2",
		"AT+CIPSERVER=1,0",
		`AT+CIPSERVER=1,80,"SSL"`,
		"AT+CIPSERVER=1,80,TCP,extra",
	} {
		if _, err := parseCIPServer(command); err == nil {
			t.Errorf("parseCIPServer(%q) succeeded", command)
		}
	}
}

func TestParseCIPRecvData(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    cipRecvDataArguments
	}{
		{command: "AT+CIPRECVDATA=512", want: cipRecvDataArguments{id: 0, length: 512}},
		{command: "AT+CIPRECVDATA=3,4096", want: cipRecvDataArguments{id: 3, hasID: true, length: 4096}},
	} {
		got, err := parseCIPRecvData(tc.command)
		if err != nil {
			t.Fatalf("parseCIPRecvData(%q): %v", tc.command, err)
		}
		if got != tc.want {
			t.Fatalf("parseCIPRecvData(%q) = %+v, want %+v", tc.command, got, tc.want)
		}
	}
	for _, command := range []string{
		"AT+CIPRECVDATA=0",
		"AT+CIPRECVDATA=5,10",
		"AT+CIPRECVDATA=0,1,2",
		"AT+CIPRECVDATA=0,2147483648",
	} {
		if _, err := parseCIPRecvData(command); err == nil {
			t.Errorf("parseCIPRecvData(%q) succeeded", command)
		}
	}
}

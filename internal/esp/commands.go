package esp

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
)

var (
	responseOK         = []byte("\r\nOK\r\n")
	responseError      = []byte("\r\nERROR\r\n")
	responseSendPrompt = []byte("\r\nOK\r\n>")
	responseSendOK     = []byte("\r\nSEND OK\r\n")
	responseSendFail   = []byte("\r\nSEND FAIL\r\n")
	responseConnected  = []byte("\r\nCONNECT\r\n\r\nOK\r\n")
	responseClosed     = []byte("\r\nCLOSED\r\n\r\nOK\r\n")
	responseReset      = []byte("\r\nOK\r\nWIFI CONNECTED\r\nWIFI GOT IP\r\n\r\nready\r\n")
)

type commandResult struct {
	response      []byte
	responseLog   string
	known         bool
	rejected      string
	setEcho       *bool
	reset         bool
	payloadLength int
}

func (e *Emulator) executeCommand(ctx context.Context, command string) commandResult {
	upper := strings.ToUpper(command)
	switch upper {
	case "AT":
		return accepted(responseOK, "OK")
	case "ATE0":
		value := false
		result := accepted(responseOK, "OK")
		result.setEcho = &value
		return result
	case "ATE1":
		value := true
		result := accepted(responseOK, "OK")
		result.setEcho = &value
		return result
	case "AT+GMR":
		response := "\r\nAT version:1.0.0.0(nextnet)\r\n" +
			"SDK version:nextnet\r\n" +
			"nextnet version:" + e.config.Version + "\r\n\r\nOK\r\n"
		return accepted([]byte(response), "version information + OK")
	case "AT+RST":
		if e.socketManager != nil {
			e.socketManager.Close()
		}
		result := accepted(responseReset, "OK + WIFI CONNECTED + WIFI GOT IP + ready")
		result.reset = true
		return result
	case "AT+CIPMUX?":
		return accepted([]byte("\r\n+CIPMUX:0\r\n\r\nOK\r\n"), "+CIPMUX:0 + OK")
	case "AT+CIPMUX=0":
		e.mux = false
		return accepted(responseOK, "OK")
	case "AT+CIPMUX=1":
		return rejected(responseError, "ERROR", "multiplexed sockets are not implemented")
	case "AT+CIPCLOSE":
		if e.socketManager == nil || !e.socketManager.Close() {
			return rejected(responseError, "ERROR", "no active connection")
		}
		e.logger.Info("socket closed", "id", 0, "cause", "AT+CIPCLOSE")
		return accepted(responseClosed, "CLOSED + OK")
	}

	if result, matched := e.executeUARTCommand(command, upper); matched {
		return result
	}
	if strings.HasPrefix(upper, "AT+CIPSTART=") {
		return e.executeCIPStart(ctx, command)
	}
	if strings.HasPrefix(upper, "AT+CIPSEND=") {
		return e.executeCIPSend(command)
	}
	return commandResult{response: responseError, responseLog: "ERROR"}
}

func (e *Emulator) executeUARTCommand(command, upper string) (commandResult, bool) {
	queries := map[string]string{
		"AT+UART_CUR?": "+UART_CUR",
		"AT+UART_DEF?": "+UART_DEF",
		"AT+UART?":     "+UART",
	}
	if label, ok := queries[upper]; ok {
		response := fmt.Sprintf("\r\n%s:%d,8,1,0,0\r\n\r\nOK\r\n", label, e.config.Baud)
		return accepted([]byte(response), label+" + OK"), true
	}

	for _, prefix := range []string{"AT+UART_CUR=", "AT+UART_DEF=", "AT+UART="} {
		if !strings.HasPrefix(upper, prefix) {
			continue
		}
		settings, err := parseUARTSettings(command[len(prefix):])
		if err != nil {
			return rejected(responseError, "ERROR", err.Error()), true
		}
		want := uartSettings{baud: e.config.Baud, dataBits: 8, stopBits: 1, parity: 0, flowControl: 0}
		if settings != want {
			return rejected(responseError, "ERROR",
				fmt.Sprintf("fixed UART configuration is %d,8,1,0,0", e.config.Baud)), true
		}
		return accepted(responseOK, "OK"), true
	}
	return commandResult{}, false
}

func (e *Emulator) executeCIPStart(ctx context.Context, command string) commandResult {
	host, port, err := parseCIPStart(command)
	if err != nil {
		return rejected(responseError, "ERROR", err.Error())
	}
	if e.socketManager == nil {
		return rejected(responseError, "ERROR", "socket manager is unavailable")
	}
	if err := e.socketManager.StartTCP(ctx, host, port); err != nil {
		e.logger.Warn("socket connect failed", "host", host, "port", port, "error", err)
		return rejected(responseError, "ERROR", err.Error())
	}
	e.logger.Info("socket connected", "id", 0, "network", "tcp", "host", host, "port", port)
	return accepted(responseConnected, "CONNECT + OK")
}

func (e *Emulator) executeCIPSend(command string) commandResult {
	if e.socketManager == nil || !e.socketManager.Connected() {
		return rejected(responseError, "ERROR", "no active connection")
	}
	separator := strings.IndexByte(command, '=')
	if separator == -1 {
		return rejected(responseError, "ERROR", "missing payload length")
	}
	length, err := strconv.Atoi(strings.TrimSpace(command[separator+1:]))
	if err != nil || length <= 0 {
		return rejected(responseError, "ERROR", "payload length must be a positive integer")
	}
	if length > e.config.MaxSendSize {
		return rejected(responseError, "ERROR",
			fmt.Sprintf("payload length %d exceeds limit %d", length, e.config.MaxSendSize))
	}
	result := accepted(responseSendPrompt, "OK + prompt")
	result.payloadLength = length
	return result
}

type uartSettings struct {
	baud        int
	dataBits    int
	stopBits    int
	parity      int
	flowControl int
}

func parseUARTSettings(value string) (uartSettings, error) {
	fields := strings.Split(value, ",")
	if len(fields) != 5 {
		return uartSettings{}, fmt.Errorf("UART setting requires five fields")
	}
	parsed := make([]int, len(fields))
	for index, field := range fields {
		value, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil {
			return uartSettings{}, fmt.Errorf("invalid UART setting field %q", field)
		}
		parsed[index] = value
	}
	return uartSettings{
		baud: parsed[0], dataBits: parsed[1], stopBits: parsed[2],
		parity: parsed[3], flowControl: parsed[4],
	}, nil
}

func parseCIPStart(command string) (string, int, error) {
	separator := strings.IndexByte(command, '=')
	if separator == -1 {
		return "", 0, fmt.Errorf("missing CIPSTART arguments")
	}
	reader := csv.NewReader(strings.NewReader(command[separator+1:]))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	fields, err := reader.Read()
	if err != nil {
		return "", 0, fmt.Errorf("parse CIPSTART: %w", err)
	}
	if _, err := reader.Read(); err != io.EOF {
		return "", 0, fmt.Errorf("CIPSTART contains extra records")
	}
	if len(fields) != 3 {
		return "", 0, fmt.Errorf("single-connection CIPSTART requires protocol, host, and port")
	}
	if !strings.EqualFold(strings.TrimSpace(fields[0]), "TCP") {
		return "", 0, fmt.Errorf("only TCP is implemented")
	}
	host := strings.TrimSpace(fields[1])
	if host == "" {
		return "", 0, fmt.Errorf("host must not be empty")
	}
	port, err := strconv.Atoi(strings.TrimSpace(fields[2]))
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("invalid TCP port %q", fields[2])
	}
	return host, port, nil
}

func accepted(response []byte, responseLog string) commandResult {
	return commandResult{response: response, responseLog: responseLog, known: true}
}

func rejected(response []byte, responseLog, reason string) commandResult {
	return commandResult{response: response, responseLog: responseLog, known: true, rejected: reason}
}

func redactCommand(command string) string {
	upper := strings.ToUpper(command)
	if strings.HasPrefix(upper, "AT+CWJAP") && strings.Contains(command, "=") {
		return command[:strings.Index(command, "=")+1] + "<redacted>"
	}
	return command
}

func validCommandBytes(line []byte) bool {
	for _, value := range line {
		if value < 0x20 || value > 0x7e {
			return false
		}
	}
	return true
}

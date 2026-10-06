package esp

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"nextnet/internal/sockets"
)

var (
	responseOK            = []byte("\r\nOK\r\n")
	responseError         = []byte("\r\nERROR\r\n")
	responseSendPrompt    = []byte("\r\nOK\r\n>")
	responseSendOK        = []byte("\r\nSEND OK\r\n")
	responseSendFail      = []byte("\r\nSEND FAIL\r\n")
	responseReset         = []byte("\r\nOK\r\nWIFI CONNECTED\r\nWIFI GOT IP\r\n\r\nready\r\n")
	responseWiFiConnected = []byte("\r\nWIFI CONNECTED\r\nWIFI GOT IP\r\n\r\nOK\r\n")
)

type commandResult struct {
	response            []byte
	responseLog         string
	known               bool
	rejected            string
	setEcho             *bool
	reset               bool
	setBaud             int
	enterTransparent    bool
	payloadLength       int
	payloadConnectionID int
}

type cipStartArguments struct {
	id           int
	hasID        bool
	host         string
	port         int
	keepAlive    int
	hasKeepAlive bool
}

type cipSendArguments struct {
	id     int
	hasID  bool
	length int
}

type cipServerArguments struct {
	mode             int
	port             int
	closeConnections bool
}

type cipRecvDataArguments struct {
	id     int
	hasID  bool
	length int
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
			e.socketManager.Shutdown()
		}
		result := accepted(responseReset, "OK + WIFI CONNECTED + WIFI GOT IP + ready")
		result.reset = true
		if e.currentBaud != e.defaultBaud {
			result.setBaud = e.defaultBaud
		}
		return result
	case "AT+CIFSR":
		return e.executeCIFSR()
	case "AT+CWLAP":
		return e.executeCWLAP()
	case "AT+CWJAP":
		return accepted(responseWiFiConnected, "WIFI CONNECTED + WIFI GOT IP + OK")
	case "AT+CIPMUX?":
		value := 0
		if e.mux {
			value = 1
		}
		response := fmt.Sprintf("\r\n+CIPMUX:%d\r\n\r\nOK\r\n", value)
		return accepted([]byte(response), fmt.Sprintf("+CIPMUX:%d + OK", value))
	case "AT+CIPMUX=0":
		return e.executeCIPMux(false)
	case "AT+CIPMUX=1":
		return e.executeCIPMux(true)
	case "AT+CIPDINFO?":
		return modeQueryResult("CIPDINFO", e.cipdInfo)
	case "AT+CIPDINFO=0":
		e.cipdInfo = false
		return accepted(responseOK, "OK")
	case "AT+CIPDINFO=1":
		e.cipdInfo = true
		return accepted(responseOK, "OK")
	case "AT+CIPRECVMODE?":
		return modeQueryResult("CIPRECVMODE", e.passiveReceive)
	case "AT+CIPRECVMODE=0":
		for id := sockets.MinConnectionID; id <= sockets.MaxConnectionID; id++ {
			if len(e.passiveData[id]) > 0 {
				return rejected(responseError, "ERROR", "cannot disable passive receive while data is buffered")
			}
		}
		e.passiveReceive = false
		e.passiveCond.Broadcast()
		return accepted(responseOK, "OK")
	case "AT+CIPRECVMODE=1":
		e.passiveReceive = true
		return accepted(responseOK, "OK")
	case "AT+CIPRECVLEN?":
		return e.executeCIPRecvLen()
	case "AT+CIPMODE?":
		return modeQueryResult("CIPMODE", e.transparentConfigured)
	case "AT+CIPMODE=0":
		e.transparentConfigured = false
		e.transparentActive.Store(false)
		return accepted(responseOK, "OK")
	case "AT+CIPMODE=1":
		if e.mux {
			return rejected(responseError, "ERROR", "transparent mode requires CIPMUX=0")
		}
		e.transparentConfigured = true
		return accepted(responseOK, "OK")
	case "AT+CIPSTATUS":
		return e.executeCIPStatus()
	case "AT+CIPSERVER?":
		return e.executeCIPServerQuery()
	case "AT+CIPSTO?":
		response := fmt.Sprintf("\r\n+CIPSTO:%d\r\n\r\nOK\r\n", e.serverTimeout)
		return accepted([]byte(response), fmt.Sprintf("+CIPSTO:%d + OK", e.serverTimeout))
	case "AT+CIPSEND":
		return e.executeTransparentCIPSend()
	case "AT+CIPCLOSE":
		if e.mux {
			return rejected(responseError, "ERROR", "multiplexed CIPCLOSE requires a connection ID")
		}
		return e.executeCIPClose(0)
	}

	if result, matched := e.executeUARTCommand(command, upper); matched {
		return result
	}
	if result, matched := e.executeCWMODECommand(command, upper); matched {
		return result
	}
	if result, matched := e.executeCWJAPCommand(command, upper); matched {
		return result
	}
	if strings.HasPrefix(upper, "AT+CIPSTART=") {
		return e.executeCIPStart(ctx, command)
	}
	if strings.HasPrefix(upper, "AT+CIPSEND=") {
		return e.executeCIPSend(command)
	}
	if strings.HasPrefix(upper, "AT+CIPCLOSE=") {
		return e.executeMuxCIPClose(command)
	}
	if strings.HasPrefix(upper, "AT+CIPSERVER=") {
		return e.executeCIPServer(ctx, command)
	}
	if strings.HasPrefix(upper, "AT+CIPRECVDATA=") {
		return e.executeCIPRecvData(command)
	}
	if strings.HasPrefix(upper, "AT+CIPSTO=") {
		return e.executeCIPServerTimeout(command)
	}
	for _, prefix := range []string{"AT+CIPDINFO=", "AT+CIPRECVMODE=", "AT+CIPMODE="} {
		if strings.HasPrefix(upper, prefix) {
			return rejected(responseError, "ERROR", "mode must be 0 or 1")
		}
	}
	return commandResult{response: responseError, responseLog: "ERROR"}
}

func (e *Emulator) executeCWMODECommand(command, upper string) (commandResult, bool) {
	queries := map[string]string{
		"AT+CWMODE?":     "+CWMODE",
		"AT+CWMODE_CUR?": "+CWMODE_CUR",
		"AT+CWMODE_DEF?": "+CWMODE_DEF",
	}
	if label, ok := queries[upper]; ok {
		response := fmt.Sprintf("\r\n%s:1\r\n\r\nOK\r\n", label)
		return accepted([]byte(response), label+":1 + OK"), true
	}

	for _, prefix := range []string{"AT+CWMODE=", "AT+CWMODE_CUR=", "AT+CWMODE_DEF="} {
		if !strings.HasPrefix(upper, prefix) {
			continue
		}
		mode, err := strconv.Atoi(strings.TrimSpace(command[len(prefix):]))
		if err != nil || mode != 1 {
			return rejected(responseError, "ERROR", "only station mode 1 is supported"), true
		}
		return accepted(responseOK, "OK"), true
	}
	return commandResult{}, false
}

func (e *Emulator) executeCIFSR() commandResult {
	info := e.currentStationInfo()
	response := fmt.Sprintf("\r\n+CIFSR:STAIP,%q\r\n+CIFSR:STAMAC,%q\r\n\r\nOK\r\n", info.IPAddress, info.MACAddress)
	return accepted([]byte(response), "station IP and MAC + OK")
}

func (e *Emulator) executeCWLAP() commandResult {
	info := e.currentStationInfo()
	response := fmt.Sprintf("\r\n+CWLAP:(0,%q,%d,%q,%d)\r\n\r\nOK\r\n",
		e.stationSSID, virtualStationRSSI, info.MACAddress, virtualWiFiChannel)
	return accepted([]byte(response), "1 virtual access point + OK")
}

func (e *Emulator) executeCWJAPCommand(command, upper string) (commandResult, bool) {
	queries := map[string]string{
		"AT+CWJAP?":     "+CWJAP",
		"AT+CWJAP_CUR?": "+CWJAP_CUR",
		"AT+CWJAP_DEF?": "+CWJAP_DEF",
	}
	if label, ok := queries[upper]; ok {
		info := e.currentStationInfo()
		response := fmt.Sprintf("\r\n%s:%q,%q,%d,%d\r\n\r\nOK\r\n",
			label, e.stationSSID, info.MACAddress, virtualWiFiChannel, virtualStationRSSI)
		return accepted([]byte(response), label+" virtual station + OK"), true
	}

	for _, prefix := range []string{"AT+CWJAP=", "AT+CWJAP_CUR=", "AT+CWJAP_DEF="} {
		if !strings.HasPrefix(upper, prefix) {
			continue
		}
		ssid, err := parseCWJAP(command[len(prefix):])
		if err != nil {
			return rejected(responseError, "ERROR", err.Error()), true
		}
		e.stationSSID = ssid
		return accepted(responseWiFiConnected, "WIFI CONNECTED + WIFI GOT IP + OK"), true
	}
	return commandResult{}, false
}

func (e *Emulator) currentStationInfo() StationInfo {
	info, err := e.config.StationInfo()
	if err != nil {
		e.logger.Warn("station interface discovery failed", "error", err)
		return StationInfo{IPAddress: "0.0.0.0", MACAddress: "00:00:00:00:00:00"}
	}
	return info
}

func (e *Emulator) executeUARTCommand(command, upper string) (commandResult, bool) {
	queries := map[string]struct {
		label string
		baud  int
	}{
		"AT+UART_CUR?": {label: "+UART_CUR", baud: e.currentBaud},
		"AT+UART_DEF?": {label: "+UART_DEF", baud: e.defaultBaud},
		"AT+UART?":     {label: "+UART", baud: e.currentBaud},
	}
	if query, ok := queries[upper]; ok {
		response := fmt.Sprintf("\r\n%s:%d,8,1,0,0\r\n\r\nOK\r\n", query.label, query.baud)
		return accepted([]byte(response), query.label+" + OK"), true
	}

	for _, prefix := range []string{"AT+UART_CUR=", "AT+UART_DEF=", "AT+UART="} {
		if !strings.HasPrefix(upper, prefix) {
			continue
		}
		settings, err := parseUARTSettings(command[len(prefix):])
		if err != nil {
			return rejected(responseError, "ERROR", err.Error()), true
		}
		if settings.dataBits != 8 || settings.stopBits != 1 || settings.parity != 0 || settings.flowControl != 0 {
			return rejected(responseError, "ERROR",
				"only 8 data bits, 1 stop bit, no parity, and no flow control are supported"), true
		}
		if settings.baud < 80 || settings.baud > 5_000_000 {
			return rejected(responseError, "ERROR", "UART baud must be between 80 and 5000000"), true
		}

		if prefix == "AT+UART_DEF=" {
			if settings.baud != e.defaultBaud {
				return rejected(responseError, "ERROR",
					fmt.Sprintf("persistent UART baud changes are not implemented; default is %d", e.defaultBaud)), true
			}
			return accepted(responseOK, "OK"), true
		}
		if settings.baud == e.currentBaud {
			return accepted(responseOK, "OK"), true
		}
		if e.config.SetBaud == nil {
			return rejected(responseError, "ERROR", "serial transport does not support baud changes"), true
		}
		result := accepted(responseOK, "OK")
		result.setBaud = settings.baud
		return result, true
	}
	return commandResult{}, false
}

func (e *Emulator) executeCIPMux(enabled bool) commandResult {
	if enabled && e.transparentConfigured {
		return rejected(responseError, "ERROR", "CIPMUX=1 is incompatible with transparent mode")
	}
	if e.mux != enabled && e.socketManager != nil {
		if _, serverRunning := e.socketManager.ServerPort(); serverRunning {
			return rejected(responseError, "ERROR", "cannot change CIPMUX while a TCP server is active")
		}
		for id := sockets.MinConnectionID; id <= sockets.MaxConnectionID; id++ {
			if e.socketManager.InUse(id) {
				return rejected(responseError, "ERROR", "cannot change CIPMUX while connections are active")
			}
		}
	}
	e.mux = enabled
	return accepted(responseOK, "OK")
}

func (e *Emulator) executeCIPStart(ctx context.Context, command string) commandResult {
	arguments, err := parseCIPStart(command)
	if err != nil {
		return rejected(responseError, "ERROR", err.Error())
	}
	if arguments.hasID != e.mux {
		if e.mux {
			return rejected(responseError, "ERROR", "multiplexed CIPSTART requires a connection ID")
		}
		return rejected(responseError, "ERROR", "single-connection CIPSTART must not include an ID")
	}
	if e.socketManager == nil {
		return rejected(responseError, "ERROR", "socket manager is unavailable")
	}
	link := sockets.Link{
		ID:           arguments.id,
		Multiplexed:  e.mux,
		RemoteHost:   arguments.host,
		RemotePort:   arguments.port,
		KeepAlive:    arguments.keepAlive,
		HasKeepAlive: arguments.hasKeepAlive,
	}
	if err := e.socketManager.StartTCP(ctx, link, arguments.host, arguments.port); err != nil {
		e.logger.Warn("socket connect failed",
			"id", arguments.id,
			"host", arguments.host,
			"port", arguments.port,
			"error", err,
		)
		return rejected(responseError, "ERROR", err.Error())
	}
	e.logger.Info("socket connected",
		"id", arguments.id,
		"network", "tcp",
		"host", arguments.host,
		"port", arguments.port,
		"keep_alive", arguments.keepAlive,
	)
	return accepted(connectionResponse(arguments.id, "CONNECT", e.mux), connectionResponseLog(arguments.id, "CONNECT", e.mux)+" + OK")
}

func (e *Emulator) executeTransparentCIPSend() commandResult {
	if e.mux {
		return rejected(responseError, "ERROR", "transparent mode requires CIPMUX=0")
	}
	if !e.transparentConfigured {
		return rejected(responseError, "ERROR", "AT+CIPMODE=1 is required before bare CIPSEND")
	}
	if e.socketManager == nil || !e.socketManager.Connected(0) {
		return rejected(responseError, "ERROR", "connection ID 0 is not active")
	}
	result := accepted(responseSendPrompt, "OK + prompt")
	result.enterTransparent = true
	return result
}

func (e *Emulator) executeCIPSend(command string) commandResult {
	arguments, err := parseCIPSend(command)
	if err != nil {
		return rejected(responseError, "ERROR", err.Error())
	}
	if arguments.hasID != e.mux {
		if e.mux {
			return rejected(responseError, "ERROR", "multiplexed CIPSEND requires a connection ID")
		}
		return rejected(responseError, "ERROR", "single-connection CIPSEND must not include an ID")
	}
	if arguments.length > e.config.MaxSendSize {
		return rejected(responseError, "ERROR",
			fmt.Sprintf("payload length %d exceeds limit %d", arguments.length, e.config.MaxSendSize))
	}
	if e.socketManager == nil || !e.socketManager.Connected(arguments.id) {
		return rejected(responseError, "ERROR", fmt.Sprintf("connection ID %d is not active", arguments.id))
	}
	result := accepted(responseSendPrompt, "OK + prompt")
	result.payloadLength = arguments.length
	result.payloadConnectionID = arguments.id
	return result
}

func (e *Emulator) executeMuxCIPClose(command string) commandResult {
	if !e.mux {
		return rejected(responseError, "ERROR", "single-connection CIPCLOSE must not include an ID")
	}
	separator := strings.IndexByte(command, '=')
	if separator == -1 {
		return rejected(responseError, "ERROR", "missing connection ID")
	}
	if strings.TrimSpace(command[separator+1:]) == "5" {
		closed := 0
		if e.socketManager != nil {
			closed = e.socketManager.CloseAll()
		}
		e.clearAllPassiveData()
		e.logger.Info("sockets closed", "cause", "AT+CIPCLOSE=5", "count", closed)
		return accepted(responseOK, "OK")
	}
	id, err := parseConnectionID(strings.TrimSpace(command[separator+1:]))
	if err != nil {
		return rejected(responseError, "ERROR", err.Error())
	}
	return e.executeCIPClose(id)
}

func (e *Emulator) executeCIPClose(id int) commandResult {
	if e.socketManager == nil || !e.socketManager.InUse(id) {
		return rejected(responseError, "ERROR", fmt.Sprintf("connection ID %d is not active", id))
	}
	e.socketManager.Close(id)
	e.clearPassiveData(id)
	e.logger.Info("socket closed", "id", id, "cause", "AT+CIPCLOSE")
	multiplexed := e.mux
	return accepted(connectionResponse(id, "CLOSED", multiplexed), connectionResponseLog(id, "CLOSED", multiplexed)+" + OK")
}

func (e *Emulator) executeCIPStatus() commandResult {
	var statuses []sockets.Status
	if e.socketManager != nil {
		statuses = e.socketManager.Statuses()
	}
	state := 2
	if len(statuses) > 0 {
		state = 3
	}
	var response strings.Builder
	response.WriteString("\r\nSTATUS:")
	response.WriteString(strconv.Itoa(state))
	response.WriteString("\r\n")
	for _, status := range statuses {
		connectionType := 0
		if status.Inbound {
			connectionType = 1
		}
		fmt.Fprintf(&response, "+CIPSTATUS:%d,%q,%q,%d,%d,%d\r\n",
			status.ID,
			status.Network,
			status.RemoteHost,
			status.RemotePort,
			status.LocalPort,
			connectionType,
		)
	}
	response.WriteString("\r\nOK\r\n")
	return accepted([]byte(response.String()), fmt.Sprintf("STATUS:%d + %d connection(s) + OK", state, len(statuses)))
}

func (e *Emulator) executeCIPServerQuery() commandResult {
	if e.socketManager != nil {
		if port, running := e.socketManager.ServerPort(); running {
			response := fmt.Sprintf("\r\n+CIPSERVER:1,%d,\"TCP\"\r\n\r\nOK\r\n", port)
			return accepted([]byte(response), fmt.Sprintf("+CIPSERVER:1,%d,\"TCP\" + OK", port))
		}
	}
	return accepted([]byte("\r\n+CIPSERVER:0\r\n\r\nOK\r\n"), "+CIPSERVER:0 + OK")
}

func (e *Emulator) executeCIPServer(ctx context.Context, command string) commandResult {
	arguments, err := parseCIPServer(command)
	if err != nil {
		return rejected(responseError, "ERROR", err.Error())
	}
	if e.socketManager == nil {
		return rejected(responseError, "ERROR", "socket manager is unavailable")
	}
	if arguments.mode == 0 {
		e.socketManager.StopTCPServer(arguments.closeConnections)
		if arguments.closeConnections {
			e.clearAllPassiveData()
		}
		e.logger.Info("TCP server stopped", "close_connections", arguments.closeConnections)
		return accepted(responseOK, "OK")
	}
	if !e.mux {
		return rejected(responseError, "ERROR", "TCP server requires CIPMUX=1")
	}
	if e.transparentConfigured {
		return rejected(responseError, "ERROR", "TCP server is incompatible with transparent mode")
	}
	if err := e.socketManager.StartTCPServer(ctx, arguments.port); err != nil {
		e.logger.Warn("TCP server start failed", "port", arguments.port, "error", err)
		return rejected(responseError, "ERROR", err.Error())
	}
	e.logger.Info("TCP server started", "network", "tcp", "port", arguments.port)
	return accepted(responseOK, "OK")
}

func (e *Emulator) executeCIPServerTimeout(command string) commandResult {
	separator := strings.IndexByte(command, '=')
	if separator == -1 {
		return rejected(responseError, "ERROR", "missing CIPSTO timeout")
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(command[separator+1:]))
	if err != nil || seconds < 0 || seconds > 7200 {
		return rejected(responseError, "ERROR", "CIPSTO timeout must be between 0 and 7200 seconds")
	}
	e.serverTimeout = seconds
	if e.socketManager != nil {
		e.socketManager.SetServerTimeout(time.Duration(seconds) * time.Second)
	}
	return accepted(responseOK, "OK")
}

func (e *Emulator) executeCIPRecvLen() commandResult {
	response := []byte("\r\n+CIPRECVLEN:")
	for id := sockets.MinConnectionID; id <= sockets.MaxConnectionID; id++ {
		if id > sockets.MinConnectionID {
			response = append(response, ',')
		}
		response = strconv.AppendInt(response, int64(len(e.passiveData[id])), 10)
	}
	response = append(response, "\r\n\r\nOK\r\n"...)
	return accepted(response, "buffered socket lengths + OK")
}

func (e *Emulator) executeCIPRecvData(command string) commandResult {
	if !e.passiveReceive {
		return rejected(responseError, "ERROR", "CIPRECVMODE=1 is required")
	}
	arguments, err := parseCIPRecvData(command)
	if err != nil {
		return rejected(responseError, "ERROR", err.Error())
	}
	if arguments.hasID != e.mux {
		if e.mux {
			return rejected(responseError, "ERROR", "multiplexed CIPRECVDATA requires a connection ID")
		}
		return rejected(responseError, "ERROR", "single-connection CIPRECVDATA must not include an ID")
	}
	if e.socketManager == nil || (!e.socketManager.InUse(arguments.id) && len(e.passiveData[arguments.id]) == 0) {
		return rejected(responseError, "ERROR", fmt.Sprintf("connection ID %d is not active", arguments.id))
	}

	actualLength := arguments.length
	if actualLength > len(e.passiveData[arguments.id]) {
		actualLength = len(e.passiveData[arguments.id])
	}
	link := e.passiveLinks[arguments.id]
	response := []byte("\r\n+CIPRECVDATA:")
	response = strconv.AppendInt(response, int64(actualLength), 10)
	response = append(response, ',')
	if e.cipdInfo && actualLength > 0 {
		response = strconv.AppendQuote(response, link.RemoteHost)
		response = append(response, ',')
		response = strconv.AppendInt(response, int64(link.RemotePort), 10)
		response = append(response, ',')
	}
	response = append(response, e.passiveData[arguments.id][:actualLength]...)
	e.passiveData[arguments.id] = e.passiveData[arguments.id][actualLength:]
	e.passiveNotified[arguments.id] = false
	response = append(response, "\r\n\r\nOK\r\n"...)

	remaining := len(e.passiveData[arguments.id])
	closed := e.passiveClosed[arguments.id]
	if remaining > 0 {
		e.passiveNotified[arguments.id] = true
		response = append(response, passiveIPDFrame(link, remaining)...)
	} else {
		e.clearPassiveData(arguments.id)
		if closed {
			response = append(response, connectionEvent(arguments.id, "CLOSED", link.Multiplexed)...)
		}
	}
	e.passiveCond.Broadcast()
	return accepted(response, fmt.Sprintf("+CIPRECVDATA:%d + OK", actualLength))
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

func parseCWJAP(value string) (string, error) {
	reader := csv.NewReader(strings.NewReader(value))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	fields, err := reader.Read()
	if err != nil {
		return "", fmt.Errorf("invalid CWJAP arguments")
	}
	if _, err := reader.Read(); err != io.EOF {
		return "", fmt.Errorf("invalid CWJAP arguments")
	}
	if len(fields) < 2 {
		return "", fmt.Errorf("CWJAP requires SSID and password")
	}
	ssid := fields[0]
	if ssid == "" || len(ssid) > 32 {
		return "", fmt.Errorf("CWJAP SSID must contain 1 to 32 bytes")
	}
	if len(fields[1]) > 64 {
		return "", fmt.Errorf("CWJAP password must not exceed 64 bytes")
	}
	return ssid, nil
}

func parseCIPStart(command string) (cipStartArguments, error) {
	separator := strings.IndexByte(command, '=')
	if separator == -1 {
		return cipStartArguments{}, fmt.Errorf("missing CIPSTART arguments")
	}
	reader := csv.NewReader(strings.NewReader(command[separator+1:]))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	fields, err := reader.Read()
	if err != nil {
		return cipStartArguments{}, fmt.Errorf("parse CIPSTART: %w", err)
	}
	if _, err := reader.Read(); err != io.EOF {
		return cipStartArguments{}, fmt.Errorf("CIPSTART contains extra records")
	}

	arguments := cipStartArguments{id: 0}
	protocolIndex := 0
	keepAliveIndex := -1
	switch len(fields) {
	case 3:
		// Single connection without TCP keepalive.
	case 4:
		// Four fields are ambiguous in ESP-AT: a numeric first field is a
		// multiplexed link ID, while a protocol first field means the last
		// field is the single-connection TCP keepalive interval.
		if _, parseErr := strconv.Atoi(strings.TrimSpace(fields[0])); parseErr == nil {
			arguments.hasID = true
			arguments.id, err = parseConnectionID(strings.TrimSpace(fields[0]))
			if err != nil {
				return cipStartArguments{}, err
			}
			protocolIndex = 1
		} else {
			keepAliveIndex = 3
		}
	case 5:
		arguments.hasID = true
		arguments.id, err = parseConnectionID(strings.TrimSpace(fields[0]))
		if err != nil {
			return cipStartArguments{}, err
		}
		protocolIndex = 1
		keepAliveIndex = 4
	default:
		return cipStartArguments{}, fmt.Errorf("CIPSTART requires protocol, host, and port, with optional link ID and TCP keepalive")
	}

	if !strings.EqualFold(strings.TrimSpace(fields[protocolIndex]), "TCP") {
		return cipStartArguments{}, fmt.Errorf("only TCP is implemented")
	}
	arguments.host = strings.TrimSpace(fields[protocolIndex+1])
	if arguments.host == "" {
		return cipStartArguments{}, fmt.Errorf("host must not be empty")
	}
	arguments.port, err = strconv.Atoi(strings.TrimSpace(fields[protocolIndex+2]))
	if err != nil || arguments.port < 1 || arguments.port > 65535 {
		return cipStartArguments{}, fmt.Errorf("invalid TCP port %q", fields[protocolIndex+2])
	}
	if keepAliveIndex >= 0 {
		arguments.keepAlive, err = strconv.Atoi(strings.TrimSpace(fields[keepAliveIndex]))
		if err != nil || arguments.keepAlive < 0 || arguments.keepAlive > 7200 {
			return cipStartArguments{}, fmt.Errorf("TCP keepalive must be between 0 and 7200 seconds")
		}
		arguments.hasKeepAlive = true
	}
	return arguments, nil
}

func parseCIPSend(command string) (cipSendArguments, error) {
	separator := strings.IndexByte(command, '=')
	if separator == -1 {
		return cipSendArguments{}, fmt.Errorf("missing CIPSEND arguments")
	}
	fields := strings.Split(command[separator+1:], ",")
	arguments := cipSendArguments{id: 0}
	lengthIndex := 0
	if len(fields) == 2 {
		arguments.hasID = true
		id, err := parseConnectionID(strings.TrimSpace(fields[0]))
		if err != nil {
			return cipSendArguments{}, err
		}
		arguments.id = id
		lengthIndex = 1
	} else if len(fields) != 1 {
		return cipSendArguments{}, fmt.Errorf("CIPSEND requires a length, with an ID only in multiplexed mode")
	}
	length, err := strconv.Atoi(strings.TrimSpace(fields[lengthIndex]))
	if err != nil || length <= 0 {
		return cipSendArguments{}, fmt.Errorf("payload length must be a positive integer")
	}
	arguments.length = length
	return arguments, nil
}

func parseCIPServer(command string) (cipServerArguments, error) {
	separator := strings.IndexByte(command, '=')
	if separator == -1 {
		return cipServerArguments{}, fmt.Errorf("missing CIPSERVER arguments")
	}
	reader := csv.NewReader(strings.NewReader(command[separator+1:]))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	fields, err := reader.Read()
	if err != nil {
		return cipServerArguments{}, fmt.Errorf("parse CIPSERVER: %w", err)
	}
	if _, err := reader.Read(); err != io.EOF {
		return cipServerArguments{}, fmt.Errorf("CIPSERVER contains extra records")
	}
	if len(fields) == 0 || len(fields) > 3 {
		return cipServerArguments{}, fmt.Errorf("CIPSERVER requires a mode and optional parameter")
	}
	mode, err := strconv.Atoi(strings.TrimSpace(fields[0]))
	if err != nil || (mode != 0 && mode != 1) {
		return cipServerArguments{}, fmt.Errorf("CIPSERVER mode must be 0 or 1")
	}
	arguments := cipServerArguments{mode: mode, port: 333}
	if mode == 0 {
		if len(fields) > 2 {
			return cipServerArguments{}, fmt.Errorf("CIPSERVER=0 accepts only the optional close-connections flag")
		}
		if len(fields) == 2 {
			closeConnections, err := strconv.Atoi(strings.TrimSpace(fields[1]))
			if err != nil || (closeConnections != 0 && closeConnections != 1) {
				return cipServerArguments{}, fmt.Errorf("CIPSERVER close-connections flag must be 0 or 1")
			}
			arguments.closeConnections = closeConnections == 1
		}
		return arguments, nil
	}

	if len(fields) >= 2 {
		arguments.port, err = strconv.Atoi(strings.TrimSpace(fields[1]))
		if err != nil || arguments.port < 1 || arguments.port > 65535 {
			return cipServerArguments{}, fmt.Errorf("invalid TCP server port %q", fields[1])
		}
	}
	if len(fields) == 3 && !strings.EqualFold(strings.TrimSpace(fields[2]), "TCP") {
		return cipServerArguments{}, fmt.Errorf("only TCP servers are implemented")
	}
	return arguments, nil
}

func parseCIPRecvData(command string) (cipRecvDataArguments, error) {
	separator := strings.IndexByte(command, '=')
	if separator == -1 {
		return cipRecvDataArguments{}, fmt.Errorf("missing CIPRECVDATA arguments")
	}
	fields := strings.Split(command[separator+1:], ",")
	arguments := cipRecvDataArguments{id: 0}
	lengthIndex := 0
	if len(fields) == 2 {
		arguments.hasID = true
		id, err := parseConnectionID(strings.TrimSpace(fields[0]))
		if err != nil {
			return cipRecvDataArguments{}, err
		}
		arguments.id = id
		lengthIndex = 1
	} else if len(fields) != 1 {
		return cipRecvDataArguments{}, fmt.Errorf("CIPRECVDATA requires a length, with an ID only in multiplexed mode")
	}
	length, err := strconv.ParseInt(strings.TrimSpace(fields[lengthIndex]), 10, 32)
	if err != nil || length <= 0 {
		return cipRecvDataArguments{}, fmt.Errorf("receive length must be a positive 32-bit integer")
	}
	arguments.length = int(length)
	return arguments, nil
}

func parseConnectionID(value string) (int, error) {
	id, err := strconv.Atoi(value)
	if err != nil || id < sockets.MinConnectionID || id > sockets.MaxConnectionID {
		return 0, fmt.Errorf("connection ID must be between %d and %d", sockets.MinConnectionID, sockets.MaxConnectionID)
	}
	return id, nil
}

func connectionResponse(id int, event string, multiplexed bool) []byte {
	if multiplexed {
		return []byte(fmt.Sprintf("\r\n%d,%s\r\n\r\nOK\r\n", id, event))
	}
	return []byte("\r\n" + event + "\r\n\r\nOK\r\n")
}

func connectionEvent(id int, event string, multiplexed bool) []byte {
	if multiplexed {
		return []byte(fmt.Sprintf("\r\n%d,%s\r\n", id, event))
	}
	return []byte("\r\n" + event + "\r\n")
}

func connectionResponseLog(id int, event string, multiplexed bool) string {
	if multiplexed {
		return fmt.Sprintf("%d,%s", id, event)
	}
	return event
}

func modeQueryResult(name string, enabled bool) commandResult {
	value := 0
	if enabled {
		value = 1
	}
	response := fmt.Sprintf("\r\n+%s:%d\r\n\r\nOK\r\n", name, value)
	return accepted([]byte(response), fmt.Sprintf("+%s:%d + OK", name, value))
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

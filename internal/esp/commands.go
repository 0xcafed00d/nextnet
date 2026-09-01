package esp

import (
	"strings"
)

var (
	responseOK    = []byte("\r\nOK\r\n")
	responseError = []byte("\r\nERROR\r\n")
	responseReset = []byte("\r\nOK\r\n\r\nready\r\n")
)

type commandResult struct {
	response    []byte
	responseLog string
	supported   bool
	setEcho     *bool
	reset       bool
}

func executeCommand(command, emulatorVersion string) commandResult {
	switch strings.ToUpper(command) {
	case "AT":
		return commandResult{response: responseOK, responseLog: "OK", supported: true}
	case "ATE0":
		value := false
		return commandResult{response: responseOK, responseLog: "OK", supported: true, setEcho: &value}
	case "ATE1":
		value := true
		return commandResult{response: responseOK, responseLog: "OK", supported: true, setEcho: &value}
	case "AT+GMR":
		response := "\r\nAT version:1.0.0.0(nextnet)\r\n" +
			"SDK version:nextnet\r\n" +
			"nextnet version:" + emulatorVersion + "\r\n\r\nOK\r\n"
		return commandResult{
			response:    []byte(response),
			responseLog: "version information + OK",
			supported:   true,
		}
	case "AT+RST":
		return commandResult{
			response:    responseReset,
			responseLog: "OK + ready",
			supported:   true,
			reset:       true,
		}
	default:
		return commandResult{response: responseError, responseLog: "ERROR"}
	}
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

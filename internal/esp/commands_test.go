package esp

import "testing"

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
	for _, command := range []string{"AT", "ATE0", "ATE1", "AT+GMR", "AT+RST"} {
		if result := executeCommand(command, "test"); !result.supported {
			t.Errorf("%s was not supported", command)
		}
	}
	if result := executeCommand("AT+NOTREAL", "test"); result.supported {
		t.Fatal("unknown command was supported")
	}
}

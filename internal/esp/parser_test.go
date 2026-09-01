package esp

import (
	"errors"
	"testing"
)

func TestLineParserHandlesFragmentationAndMultipleCommands(t *testing.T) {
	parser := newLineParser(4096)
	if events := parser.feed([]byte("AT+G")); len(events) != 0 {
		t.Fatalf("fragment produced %d events", len(events))
	}
	events := parser.feed([]byte("MR\r\nAT\nATE0\r\n"))
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	for index, want := range []string{"AT+GMR", "AT", "ATE0"} {
		if got := string(events[index].line); got != want {
			t.Errorf("event %d = %q, want %q", index, got, want)
		}
	}
}

func TestLineParserBoundsInput(t *testing.T) {
	parser := newLineParser(4)
	events := parser.feed([]byte("ABCDE\r\nAT\r\n"))
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if !errors.Is(events[0].err, ErrLineTooLong) {
		t.Fatalf("first event error = %v", events[0].err)
	}
	if got := string(events[1].line); got != "AT" {
		t.Fatalf("second event = %q, want AT", got)
	}
}

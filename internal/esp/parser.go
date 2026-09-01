package esp

import "errors"

var ErrLineTooLong = errors.New("AT command exceeds maximum line length")

type parseEvent struct {
	line []byte
	err  error
}

type lineParser struct {
	maximum    int
	line       []byte
	discarding bool
	pendingLF  bool
}

func newLineParser(maximum int) *lineParser {
	return &lineParser{
		maximum: maximum,
		line:    make([]byte, 0, 128),
	}
}

func (p *lineParser) feed(input []byte) []parseEvent {
	var events []parseEvent
	for _, value := range input {
		if p.consumePendingLF(value) {
			continue
		}
		if event := p.feedByte(value); event != nil {
			events = append(events, *event)
		}
	}
	return events
}

// consumePendingLF removes the LF half of a CRLF terminator. Serve calls this
// even in payload mode so a CIPSEND payload beginning in the same serial read
// never accidentally includes the command's terminator LF.
func (p *lineParser) consumePendingLF(value byte) bool {
	if !p.pendingLF {
		return false
	}
	p.pendingLF = false
	return value == '\n'
}

func (p *lineParser) feedByte(value byte) *parseEvent {
	switch value {
	case '\r':
		p.pendingLF = true
		return p.finish()
	case '\n':
		return p.finish()
	default:
		if p.discarding {
			return nil
		}
		if len(p.line) >= p.maximum {
			p.discarding = true
			p.line = p.line[:0]
			return nil
		}
		p.line = append(p.line, value)
		return nil
	}
}

func (p *lineParser) finish() *parseEvent {
	if p.discarding {
		p.discarding = false
		p.line = p.line[:0]
		return &parseEvent{err: ErrLineTooLong}
	}
	if len(p.line) == 0 {
		return nil
	}
	line := append([]byte(nil), p.line...)
	p.line = p.line[:0]
	return &parseEvent{line: line}
}

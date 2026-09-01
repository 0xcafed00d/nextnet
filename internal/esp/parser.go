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
	lastWasCR  bool
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
		switch value {
		case '\r':
			events = p.finish(events)
			p.lastWasCR = true
		case '\n':
			if p.lastWasCR {
				p.lastWasCR = false
				continue
			}
			events = p.finish(events)
		default:
			p.lastWasCR = false
			if p.discarding {
				continue
			}
			if len(p.line) >= p.maximum {
				p.discarding = true
				p.line = p.line[:0]
				continue
			}
			p.line = append(p.line, value)
		}
	}
	return events
}

func (p *lineParser) finish(events []parseEvent) []parseEvent {
	if p.discarding {
		p.discarding = false
		p.line = p.line[:0]
		return append(events, parseEvent{err: ErrLineTooLong})
	}
	if len(p.line) == 0 {
		return events
	}
	line := append([]byte(nil), p.line...)
	p.line = p.line[:0]
	return append(events, parseEvent{line: line})
}

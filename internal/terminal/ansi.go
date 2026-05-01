package terminal

import "strings"

// StripANSI removes ANSI/VT100 escape sequences from s.
func StripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] != '\x1b' {
			b.WriteByte(s[i])
			i++
			continue
		}
		next, keep := consumeEscapeSequence(s, i)
		if next <= i {
			i++
			continue
		}
		_ = keep
		i = next
	}
	return b.String()
}

// StripTerminalQueries removes terminal query escape sequences that expect a
// response from the terminal emulator. These sequences are safe during a live
// PTY session, but replaying them later can corrupt interactive TUIs.
func StripTerminalQueries(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] != '\x1b' {
			b.WriteByte(s[i])
			i++
			continue
		}

		next, keep := consumeEscapeSequence(s, i)
		if next <= i {
			i++
			continue
		}
		if keep && !isTerminalQuery(s[i:next]) {
			b.WriteString(s[i:next])
		}
		i = next
	}
	return b.String()
}

func consumeEscapeSequence(s string, start int) (next int, keep bool) {
	if start+1 >= len(s) {
		return start + 1, false
	}

	switch s[start+1] {
	case '[': // CSI
		i := start + 2
		for i < len(s) && (s[i] < 0x40 || s[i] > 0x7E) {
			i++
		}
		if i < len(s) {
			return i + 1, true
		}
		return len(s), false
	case ']': // OSC
		i := start + 2
		for i < len(s) {
			if s[i] == '\x07' {
				return i + 1, true
			}
			if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2, true
			}
			i++
		}
		return len(s), false
	default:
		return start + 2, false
	}
}

func isTerminalQuery(seq string) bool {
	if len(seq) < 2 || seq[0] != '\x1b' {
		return false
	}

	switch seq[1] {
	case '[':
		// Device status reports / cursor position requests expect a reply.
		return seq[len(seq)-1] == 'n'
	case ']':
		body := oscBody(seq)
		// Codex emits OSC color queries like "11;?" before starting the TUI.
		return strings.Contains(body, ";?") || strings.HasSuffix(body, "?")
	default:
		return false
	}
}

func oscBody(seq string) string {
	if len(seq) < 3 || seq[0] != '\x1b' || seq[1] != ']' {
		return ""
	}
	body := seq[2:]
	switch {
	case strings.HasSuffix(body, "\x07"):
		return body[:len(body)-1]
	case strings.HasSuffix(body, "\x1b\\"):
		return body[:len(body)-2]
	default:
		return body
	}
}

// QueryAutoResponder implements a streaming detector for terminal capability
// queries (cursor position, device attributes, OSC color queries, etc.) that
// TUIs emit at startup and block on. Behind a `cw run` PTY there is no
// terminal emulator to answer those queries, so the child program hangs.
//
// QueryAutoResponder is fed bytes streamed out of the child PTY; for each
// recognized query it returns the canned response bytes the dispatcher should
// write back into the child's PTY stdin. Bytes are NOT consumed from the
// stream — callers should still forward all output to log/broadcaster
// subscribers; the responder only generates side-channel responses.
//
// Partial sequences spanning a read boundary are buffered and re-evaluated
// on the next Feed call.
type QueryAutoResponder struct {
	tail []byte
}

// NewQueryAutoResponder returns a fresh responder.
func NewQueryAutoResponder() *QueryAutoResponder {
	return &QueryAutoResponder{}
}

// Feed scans data (with any retained tail from the previous call prepended)
// for terminal capability queries and returns the concatenated response bytes
// for every query found. Any partial trailing escape sequence is retained
// internally and re-evaluated on the next call.
func (r *QueryAutoResponder) Feed(data []byte) []byte {
	if len(data) == 0 && len(r.tail) == 0 {
		return nil
	}
	var combined []byte
	if len(r.tail) > 0 {
		combined = make([]byte, 0, len(r.tail)+len(data))
		combined = append(combined, r.tail...)
		combined = append(combined, data...)
		r.tail = nil
	} else {
		combined = data
	}

	s := string(combined)
	var out []byte
	i := 0
	for i < len(s) {
		if s[i] != '\x1b' {
			i++
			continue
		}
		next, complete := consumeEscapeSequence(s, i)
		if !complete {
			r.tail = append([]byte(nil), combined[i:]...)
			return out
		}
		if resp := queryResponse(s[i:next]); len(resp) > 0 {
			out = append(out, resp...)
		}
		i = next
	}
	return out
}

// queryResponse returns the canned response bytes for known terminal
// capability queries, or nil if seq is not one we handle.
func queryResponse(seq string) []byte {
	if len(seq) < 2 || seq[0] != '\x1b' {
		return nil
	}
	switch seq[1] {
	case '[':
		switch seq {
		case "\x1b[6n":
			// Cursor position request: report row 1 col 1.
			return []byte("\x1b[1;1R")
		case "\x1b[c", "\x1b[0c":
			// Primary device attributes: VT102 minimal.
			return []byte("\x1b[?6c")
		case "\x1b[>c", "\x1b[>0c":
			// Secondary device attributes.
			return []byte("\x1b[>0;0;0c")
		case "\x1b[>q":
			// XTVERSION.
			return []byte("\x1bP>|cw\x1b\\")
		case "\x1b[?u":
			// Kitty keyboard progressive enhancement query.
			return []byte("\x1b[?0u")
		}
		return nil
	case ']':
		body := oscBody(seq)
		switch {
		case strings.HasPrefix(body, "10;?"):
			return []byte("\x1b]10;rgb:c0c0/c0c0/c0c0\x07")
		case strings.HasPrefix(body, "11;?"):
			return []byte("\x1b]11;rgb:0000/0000/0000\x07")
		case strings.HasPrefix(body, "12;?"):
			return []byte("\x1b]12;rgb:c0c0/c0c0/c0c0\x07")
		}
		return nil
	default:
		return nil
	}
}

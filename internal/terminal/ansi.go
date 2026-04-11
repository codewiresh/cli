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

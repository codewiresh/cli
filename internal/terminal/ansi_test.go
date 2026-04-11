package terminal

import "testing"

func TestStripANSI(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{"plain text", "hello world", "hello world"},
		{"CSI color", "\x1b[32mgreen\x1b[0m text", "green text"},
		{"CSI cursor move", "\x1b[2J\x1b[H", ""},
		{"OSC title BEL", "\x1b]0;title\x07rest", "rest"},
		{"OSC title ST", "\x1b]0;title\x1b\\rest", "rest"},
		{"nested codes", "\x1b[1;32mBold\x1b[0m normal", "Bold normal"},
		{"preserves newlines", "line1\nline2\n", "line1\nline2\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripANSI(tc.input)
			if got != tc.want {
				t.Errorf("StripANSI(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestStripTerminalQueries(t *testing.T) {
	input := "Welcome\x1b]11;?\x1b\\\x1b[6n back\x1b[32m green\x1b[0m"
	want := "Welcome back\x1b[32m green\x1b[0m"
	if got := StripTerminalQueries(input); got != want {
		t.Fatalf("StripTerminalQueries(%q) = %q, want %q", input, got, want)
	}
}

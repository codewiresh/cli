package node

import termutil "github.com/codewiresh/codewire/internal/terminal"

func stripANSI(s string) string {
	return termutil.StripANSI(s)
}

func sanitizeReplayOutput(s string) string {
	return termutil.StripTerminalQueries(s)
}

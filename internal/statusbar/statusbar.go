package statusbar

import (
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var barStyle = lipgloss.NewRenderer(os.Stdout).NewStyle().Reverse(true)

type StatusBar struct {
	SessionID uint32
	Status    string
	Started   time.Time
	Rows      uint16
	Cols      uint16
	Enabled   bool
	suspended bool
	active    bool
}

func New(sessionID uint32, cols, rows uint16) *StatusBar {
	return &StatusBar{
		SessionID: sessionID,
		Status:    "running",
		Started:   time.Now(),
		Rows:      rows,
		Cols:      cols,
		Enabled:   rows >= 5,
	}
}

// PtySize returns the PTY size to report to the node.
// One row shorter when the status bar is enabled.
func (s *StatusBar) PtySize() (cols, rows uint16) {
	if s.Enabled && !s.suspended {
		return s.Cols, s.Rows - 1
	}
	return s.Cols, s.Rows
}

// Setup sets the scroll region and draws the initial status bar.
func (s *StatusBar) Setup() []byte {
	return s.Draw()
}

func (s *StatusBar) canRender() bool {
	return s.Enabled && !s.suspended
}

func (s *StatusBar) activate() []byte {
	if !s.canRender() || s.active {
		return nil
	}
	s.active = true
	// Set scroll region to rows 1..(Rows-1), protecting the last row for the bar.
	return []byte(fmt.Sprintf("\x1b[1;%dr", s.Rows-1))
}

// Suspend disables the bar until Resume is called.
func (s *StatusBar) Suspend() []byte {
	if s.suspended {
		return nil
	}
	s.suspended = true
	if !s.active {
		return nil
	}
	s.active = false
	var out []byte
	// Restore scrolling to the full terminal and clear the bar row.
	out = append(out, "\x1b[r"...)
	out = append(out, "\x1b7"...)
	out = append(out, fmt.Sprintf("\x1b[%d;1H", s.Rows)...)
	out = append(out, "\x1b[2K"...)
	out = append(out, "\x1b8"...)
	return out
}

// Resume re-enables the bar after Suspend.
func (s *StatusBar) Resume() {
	s.suspended = false
}

// Teardown removes only terminal state the bar can safely own.
// It intentionally leaves the last row untouched so detach does not erase
// user-visible output that landed there after the bar rendered.
func (s *StatusBar) Teardown() []byte {
	if !s.active {
		return nil
	}
	s.active = false
	return []byte("[r")
}

// Draw renders the status bar (save cursor, render, restore cursor).
func (s *StatusBar) Draw() []byte {
	if !s.canRender() {
		return nil
	}
	elapsed := time.Since(s.Started)
	age := formatDuration(uint64(elapsed.Seconds()))

	content := fmt.Sprintf(" [cw] session %d | %s | %s | Ctrl+B d",
		s.SessionID, s.Status, age)

	// Pad or truncate to fill the row
	cols := int(s.Cols)
	var padded string
	if len(content) >= cols {
		padded = content[:cols]
	} else {
		padded = fmt.Sprintf("%-*s", cols, content)
	}

	var out []byte
	out = append(out, s.activate()...)
	// Save cursor
	out = append(out, "\x1b7"...)
	// Move to status bar row (last row)
	out = append(out, fmt.Sprintf("\x1b[%d;1H", s.Rows)...)
	// Reverse video + content
	out = append(out, barStyle.Render(padded)...)
	// Restore cursor
	out = append(out, "\x1b8"...)
	return out
}

// Resize updates dimensions, resets the scroll region, and redraws.
func (s *StatusBar) Resize(cols, rows uint16) []byte {
	s.Cols = cols
	s.Rows = rows
	s.Enabled = rows >= 5
	if !s.Enabled {
		s.active = false
		return nil
	}
	if s.suspended {
		return nil
	}
	s.active = false
	return s.Draw()
}

func formatDuration(secs uint64) string {
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	if secs < 3600 {
		return fmt.Sprintf("%dm", secs/60)
	}
	return fmt.Sprintf("%dh%dm", secs/3600, (secs%3600)/60)
}

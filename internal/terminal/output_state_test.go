package terminal

import "testing"

func TestOutputStateTracksAltScreen(t *testing.T) {
	state := NewOutputState()
	state.Feed([]byte("\x1b[?1049h"))
	if !state.AltScreen() {
		t.Fatal("expected alt-screen to be enabled")
	}
	if !state.SafeToInject() {
		t.Fatal("expected parser to return to ground state")
	}

	state.Feed([]byte("\x1b[?1049l"))
	if state.AltScreen() {
		t.Fatal("expected alt-screen to be disabled")
	}
}

func TestOutputStateTracksIncompleteSequences(t *testing.T) {
	state := NewOutputState()
	state.Feed([]byte("\x1b[?1049"))
	if state.SafeToInject() {
		t.Fatal("expected parser to stay unsafe mid-CSI")
	}
	state.Feed([]byte("h"))
	if !state.SafeToInject() {
		t.Fatal("expected parser to return to ground state after CSI completion")
	}
}

func TestOutputStateTracksOSC(t *testing.T) {
	state := NewOutputState()
	state.Feed([]byte("\x1b]0;title"))
	if state.SafeToInject() {
		t.Fatal("expected parser to stay unsafe mid-OSC")
	}
	state.Feed([]byte("\x07"))
	if !state.SafeToInject() {
		t.Fatal("expected parser to return to ground state after OSC")
	}
}

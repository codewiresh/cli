package statusbar

import (
	"strings"
	"testing"
)

func TestPtySizeReducesRows(t *testing.T) {
	bar := New(1, 80, 24)
	cols, rows := bar.PtySize()
	if cols != 80 || rows != 23 {
		t.Fatalf("expected (80, 23), got (%d, %d)", cols, rows)
	}
}

func TestPtySizeFullWhenDisabled(t *testing.T) {
	bar := New(1, 80, 4)
	if bar.Enabled {
		t.Fatal("should be disabled")
	}
	cols, rows := bar.PtySize()
	if cols != 80 || rows != 4 {
		t.Fatalf("expected (80, 4), got (%d, %d)", cols, rows)
	}
}

func TestSetupSetsScrollRegionAndDrawsBar(t *testing.T) {
	bar := New(1, 80, 24)
	out := string(bar.Setup())
	if strings.Contains(out, "\x1b[?1049h") {
		t.Fatal("should not contain alt screen")
	}
	if !strings.Contains(out, "\x1b[1;23r") {
		t.Fatal("should contain scroll region")
	}
	if !strings.Contains(out, "session 1") {
		t.Fatal("should contain session 1")
	}
}

func TestTeardownOnlyClearsOwnedBarState(t *testing.T) {
	bar := New(1, 80, 24)
	_ = bar.Setup()
	out := string(bar.Teardown())
	checks := map[string]string{
		"\x1b[r":     "scroll region reset",
		"\x1b[24;1H": "move to last row",
		"\x1b[2K":    "clear line",
	}
	for seq, name := range checks {
		if !strings.Contains(out, seq) {
			t.Fatalf("should contain %s (%s)", name, seq)
		}
	}
	for _, seq := range []string{"\x1b[?1049l", "\x1b[?25h", "\x1b[<u", "\x1b[?1004l", "\x1b[?1000l", "\x1b[?1006l"} {
		if strings.Contains(out, seq) {
			t.Fatalf("should not contain unrelated terminal reset %q", seq)
		}
	}
}

func TestDrawContainsSessionInfo(t *testing.T) {
	bar := New(42, 80, 24)
	out := string(bar.Draw())
	for _, s := range []string{"session 42", "running", "Ctrl+B d"} {
		if !strings.Contains(out, s) {
			t.Fatalf("should contain %q", s)
		}
	}
}

func TestDisabledProducesEmptySetupAndDraw(t *testing.T) {
	bar := New(1, 80, 3)
	if len(bar.Setup()) != 0 {
		t.Fatal("setup should be empty")
	}
	if len(bar.Draw()) != 0 {
		t.Fatal("draw should be empty")
	}
}

func TestDisabledTeardownIsEmpty(t *testing.T) {
	bar := New(1, 80, 3)
	if bar.Enabled {
		t.Fatal("should be disabled")
	}
	if out := bar.Teardown(); len(out) != 0 {
		t.Fatal("teardown should be empty when bar is disabled")
	}
}

func TestSuspendRestoresFullPtySizeAndClearsBar(t *testing.T) {
	bar := New(1, 80, 24)
	_ = bar.Setup()
	out := string(bar.Suspend())
	if !strings.Contains(out, "\x1b[r") {
		t.Fatal("should reset scroll region when suspending")
	}
	cols, rows := bar.PtySize()
	if cols != 80 || rows != 24 {
		t.Fatalf("expected full size while suspended, got (%d, %d)", cols, rows)
	}
}

func TestResumeAllowsBarToDrawAgain(t *testing.T) {
	bar := New(1, 80, 24)
	_ = bar.Setup()
	_ = bar.Suspend()
	bar.Resume()
	out := string(bar.Draw())
	if !strings.Contains(out, "\x1b[1;23r") {
		t.Fatal("should re-activate scroll region after resume")
	}
	if !strings.Contains(out, "session 1") {
		t.Fatal("should render the bar after resume")
	}
}

func TestFormatDurationDisplay(t *testing.T) {
	cases := []struct {
		secs uint64
		want string
	}{
		{0, "0s"},
		{45, "45s"},
		{60, "1m"},
		{300, "5m"},
		{3661, "1h1m"},
	}
	for _, c := range cases {
		got := formatDuration(c.secs)
		if got != c.want {
			t.Errorf("formatDuration(%d) = %q, want %q", c.secs, got, c.want)
		}
	}
}

package main

import (
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestWatchResizeSendsOnlyOnChange feeds a size sequence into the poller and
// expects one WindowSizeMsg per change (identical or unreadable sizes are
// skipped), and nothing at all after stop.
func TestWatchResizeSendsOnlyOnChange(t *testing.T) {
	seq := []struct {
		w, h int
		ok   bool
	}{
		{80, 24, true},
		{80, 24, true},  // unchanged
		{120, 40, true}, // grown
		{120, 40, true}, // unchanged
		{0, 0, false},   // terminal unreadable
		{40, 10, true},  // shrunk
	}

	var sizeMu sync.Mutex
	calls := 0
	size := func() (int, int, bool) {
		sizeMu.Lock()
		defer sizeMu.Unlock()
		i := min(calls, len(seq)-1)
		calls++
		return seq[i].w, seq[i].h, seq[i].ok
	}

	var msgMu sync.Mutex
	var got []tea.WindowSizeMsg
	done := make(chan struct{})
	send := func(msg tea.Msg) {
		sizeMsg, ok := msg.(tea.WindowSizeMsg)
		if !ok {
			t.Errorf("send got %T, want tea.WindowSizeMsg", msg)
			return
		}
		msgMu.Lock()
		got = append(got, sizeMsg)
		n := len(got)
		msgMu.Unlock()
		if n == 3 {
			close(done)
		}
	}

	stop := watchResizeWith(time.Millisecond, size, send)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		stop()
		t.Fatalf("timed out waiting for resize messages; got %d", len(got))
	}
	stop()

	msgMu.Lock()
	defer msgMu.Unlock()
	want := []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 120, Height: 40}, {Width: 40, Height: 10}}
	if len(got) != len(want) {
		t.Fatalf("got %d messages %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("message %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestFileSizeRejectsNil(t *testing.T) {
	if _, _, ok := fileSize(nil); ok {
		t.Fatal("fileSize(nil) should report no size")
	}
}

// TestTerminalSizeWithoutTerminal covers the fallback chain when neither
// stdout nor stderr is a terminal: it must report failure rather than a bogus
// size (the CONOUT$ branch is skipped on non-windows and only yields a size
// when the process really is attached to a console).
func TestTerminalSizeWithoutTerminal(t *testing.T) {
	if w, h, ok := terminalSize(); ok && (w <= 0 || h <= 0) {
		t.Fatalf("terminalSize() = %d,%d ok; want positive dimensions when ok", w, h)
	}
}

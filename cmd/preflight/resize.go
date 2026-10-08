package main

import (
	"os"
	"runtime"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

// resizePoll is how often the terminal size is checked while a bubbletea
// program runs.
const resizePoll = 200 * time.Millisecond

// watchResize keeps a running program in sync with the terminal size and
// returns a func that stops watching.
//
// bubbletea normally learns about a resize from SIGWINCH (unix) or from Win32
// console input events (windows). Neither fires here: the input is
// keyboardInput, which is not an *os.File, so on windows bubbletea drops out
// of the coninput reader — the only source of window-size events there, since
// Windows has no SIGWINCH — and reads plain ANSI bytes instead (see
// keyboard.go). Without events the layout keeps whatever size the program
// started with: after a shrink the view is clipped and the cursor can be
// scrolled out of sight, after a grow the new space stays empty.
//
// Polling the size and sending a WindowSizeMsg on every change keeps every
// screen in step with the terminal.
func watchResize(p *tea.Program) func() {
	return watchResizeWith(resizePoll, terminalSize, p.Send)
}

// watchResizeWith is watchResize with the size source and the message sink
// injected so it can be tested without a terminal.
func watchResizeWith(interval time.Duration, size func() (int, int, bool), send func(tea.Msg)) func() {
	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		lastW, lastH := 0, 0
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				w, h, ok := size()
				if !ok || (w == lastW && h == lastH) {
					continue
				}
				lastW, lastH = w, h
				send(tea.WindowSizeMsg{Width: w, Height: h})
			}
		}
	}()

	return func() {
		close(stop)
		<-done
	}
}

// terminalSize reports the size of the terminal the output is attached to.
// os.Stdout is the usual case; when it is not a console (output piped through
// a wrapper such as git bash) fall back to stderr and, on windows, to the
// console the process is attached to.
func terminalSize() (int, int, bool) {
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		if w, h, ok := fileSize(f); ok {
			return w, h, true
		}
	}
	if runtime.GOOS != "windows" {
		return 0, 0, false
	}
	f, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		return 0, 0, false
	}
	defer func() { _ = f.Close() }()
	return fileSize(f)
}

// fileSize returns the visible size of the terminal behind f. It reports false
// when f is not a terminal or the size cannot be read.
func fileSize(f *os.File) (int, int, bool) {
	if f == nil {
		return 0, 0, false
	}
	w, h, err := term.GetSize(int(f.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

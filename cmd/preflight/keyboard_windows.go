//go:build windows

package main

import (
	"errors"
	"io"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// winConsole implements consoleHook on Windows.
//
// os.Stdin.Read cannot be interrupted: a read-loop goroutine blocked in it
// survives program shutdown (bubbletea's fallback cancelreader cannot cancel
// a console read; charmbracelet/bubbletea#1167) and then steals the first
// keypress of the next program run in this process — or of the next process
// reading the same console (ssh). Instead, winConsole reads CONIN$ with an
// overlapped ReadFile and waits on the console handle together with a cancel
// event, so close() ends a pending read deterministically.
//
// ReadFile on a console input handle behaves like ReadConsole (character
// based), so the bytes match what os.Stdin.Read delivers: raw VT sequences,
// CSI-u included, pass through to the translator untouched.
type winConsole struct {
	conin       windows.Handle
	cancelEvent windows.Handle
	origMode    uint32
	restoreMode bool

	canceled atomic.Bool
	exited   atomic.Bool
}

// newWinConsole opens CONIN$ in overlapped mode and prepares the console
// input mode. Returns an error when stdin is not a console (e.g. piped),
// in which case the caller falls back to a plain os.File read.
func newWinConsole() (*winConsole, error) {
	path, err := syscall.UTF16PtrFromString("CONIN$")
	if err != nil {
		return nil, err
	}
	conin, err := windows.CreateFile(path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, err
	}
	cancelEvent, err := windows.CreateEvent(nil, 1, 0, nil) // manual-reset
	if err != nil {
		_ = windows.CloseHandle(conin)
		return nil, err
	}
	c := &winConsole{conin: conin, cancelEvent: cancelEvent}

	// Input mode is per console buffer. Mouse/window input events signal the
	// handle without producing characters, which would leave a ReadFile
	// pending forever; keep them off (mouse still works via SGR byte
	// sequences). Enable VT input so arrows and CSI-u sequences arrive as
	// escape bytes. Flush leftover records so typed-ahead input from a
	// previous session cannot bleed into this program.
	var mode uint32
	if err := windows.GetConsoleMode(conin, &mode); err == nil {
		c.origMode = mode
		c.restoreMode = true
		mode &^= windows.ENABLE_MOUSE_INPUT
		mode &^= windows.ENABLE_WINDOW_INPUT
		mode |= windows.ENABLE_VIRTUAL_TERMINAL_INPUT | windows.ENABLE_EXTENDED_FLAGS
		if err := windows.SetConsoleMode(conin, mode); err == nil {
			_ = windows.FlushConsoleInputBuffer(conin)
		}
	}
	return c, nil
}

func (c *winConsole) read(buf []byte) (n int, err error) {
	defer c.exited.Store(true)
	for {
		if c.canceled.Load() {
			return 0, io.EOF
		}
		ev, err := windows.WaitForMultipleObjects(
			[]windows.Handle{c.conin, c.cancelEvent}, false, windows.INFINITE)
		if err != nil {
			return 0, err
		}
		switch ev {
		case windows.WAIT_OBJECT_0: // console has input
		case windows.WAIT_OBJECT_0 + 1: // canceled
			return 0, io.EOF
		default:
			return 0, errors.New("console input wait failed")
		}

		hevent, err := windows.CreateEvent(nil, 0, 0, nil)
		if err != nil {
			return 0, err
		}
		overlapped := windows.Overlapped{HEvent: hevent}
		var read uint32
		err = windows.ReadFile(c.conin, buf, &read, &overlapped)
		if err != nil && err != windows.ERROR_IO_PENDING {
			_ = windows.CloseHandle(hevent)
			if err == windows.ERROR_OPERATION_ABORTED {
				continue
			}
			return int(read), err
		}
		ev, err = windows.WaitForMultipleObjects(
			[]windows.Handle{hevent, c.cancelEvent}, false, windows.INFINITE)
		if err != nil {
			// The wait itself failed (e.g. handle closed during shutdown):
			// abort the pending read so it cannot consume input meant for
			// the next program.
			_ = windows.CancelIoEx(c.conin, &overlapped)
			_ = windows.CloseHandle(hevent)
			return 0, err
		}
		if ev == windows.WAIT_OBJECT_0+1 {
			// Canceled while a read was pending: abort it so it cannot
			// consume input meant for the next program.
			_ = windows.CancelIoEx(c.conin, &overlapped)
			_ = windows.CloseHandle(hevent)
			return 0, io.EOF
		}
		err = windows.GetOverlappedResult(c.conin, &overlapped, &read, false)
		_ = windows.CloseHandle(hevent)
		if err == windows.ERROR_OPERATION_ABORTED {
			continue
		}
		if err != nil {
			return int(read), err
		}
		if read == 0 {
			continue
		}
		return int(read), nil
	}
}

// close cancels any pending read, waits for it to observe the cancel, then
// releases the handles and restores the console input mode.
func (c *winConsole) close() {
	c.canceled.Store(true)
	_ = windows.SetEvent(c.cancelEvent)
	for i := 0; i < 50 && !c.exited.Load(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	_ = windows.CloseHandle(c.cancelEvent)
	if c.restoreMode {
		_ = windows.SetConsoleMode(c.conin, c.origMode)
	}
	_ = windows.CloseHandle(c.conin)
}

// newConsoleHook wires the cancelable console reader on windows, falling
// back to a plain os.File read when stdin is not a console.
func newConsoleHook(inner *os.File) consoleHook {
	if c, err := newWinConsole(); err == nil {
		return c
	}
	return &plainConsole{in: inner}
}

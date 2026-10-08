package main

import (
	"bytes"
	"os"
	"strconv"
	"strings"
)

// keyboardInput wraps stdin and translates CSI-u (kitty keyboard protocol)
// and xterm modifyOtherKeys escape sequences into the legacy escape forms
// bubbletea v1.3.10 parses (ESC[A for arrows, ESC+m for Alt+M, …). Terminals
// that emit CSI-u for modified keys (kitty, wezterm, Windows Terminal with the
// enhanced keyboard protocol enabled) otherwise deliver ↓ and Alt+letter as
// unknown sequences that bubbletea silently drops.
//
// We deliberately do NOT implement Fd() on keyboardInput. On Windows, bubbletea
// checks if the input implements Fd() == os.Stdin.Fd(); if so, it discards the
// reader and engages coninput (which discards VirtualKeyCode on Alt+key presses,
// dropping Alt+M). Without Fd(), bubbletea uses cancelreader over keyboardInput,
// reading ANSI byte sequences and translating CSI-u into Alt+key events reliably.
type keyboardInput struct {
	inner   *os.File
	pending []byte      // translated bytes not yet returned
	hold    []byte      // incomplete CSI-u prefix waiting for its terminator
	plat    consoleHook // platform-specific blocking read; nil in tests
}

// consoleHook abstracts the blocking byte read behind keyboardInput so the
// windows build can make it cancelable (see keyboard_windows.go). A plain
// os.File read is the fallback used by tests and by non-console stdin.
type consoleHook interface {
	read(buf []byte) (int, error)
	close()
}

type plainConsole struct{ in *os.File }

func (c *plainConsole) read(buf []byte) (int, error) { return c.in.Read(buf) }
func (c *plainConsole) close()                       {}

// newKeyboardInput builds the reader used for every tea.Program in this app.
func newKeyboardInput(inner *os.File) *keyboardInput {
	return &keyboardInput{inner: inner, plat: newConsoleHook(inner)}
}

// shutdown cancels and tears down the platform read so the input goroutine
// cannot outlive the program: a read loop left blocked would swallow the
// first keypress of the next program (charmbracelet/bubbletea#1167).
func (k *keyboardInput) shutdown() {
	if k.plat != nil {
		k.plat.close()
	}
}

func (k *keyboardInput) rawRead(buf []byte) (int, error) {
	if k.plat != nil {
		return k.plat.read(buf)
	}
	return k.inner.Read(buf)
}

func (k *keyboardInput) Read(p []byte) (int, error) {
	if len(k.pending) > 0 {
		n := copy(p, k.pending)
		k.pending = k.pending[n:]
		return n, nil
	}
	buf := make([]byte, 256)
	n, err := k.rawRead(buf)
	if n == 0 {
		return 0, err
	}
	data := buf[:n]
	if len(k.hold) > 0 {
		data = append(k.hold, data...)
		k.hold = nil
	}
	out := translateCSIu(data, &k.hold)
	if len(k.hold) > 0 && err != nil {
		// EOF with an incomplete sequence tail: flush it as-is.
		out = append(out, k.hold...)
		k.hold = nil
	}
	if len(out) > len(p) {
		k.pending = out[len(p):]
		out = out[:len(p)]
	}
	return copy(p, out), err
}

func (k *keyboardInput) Write(p []byte) (int, error) { return k.inner.Write(p) }
func (k *keyboardInput) Close() error                { return k.inner.Close() }

// translateCSIu rewrites complete CSI-u and modifyOtherKeys sequences in data
// into legacy forms; a trailing incomplete sequence prefix is stored in *hold
// and prepended to the next chunk by the caller. All other bytes pass through untouched.
func translateCSIu(data []byte, hold *[]byte) []byte {
	out := make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		idx := bytes.Index(data[i:], []byte("\x1b["))
		if idx < 0 {
			out = append(out, data[i:]...)
			break
		}
		out = append(out, data[i:i+idx]...)
		start := i + idx
		// Look for the terminating byte (0x40..0x7E) of the CSI sequence.
		end := -1
		for j := start + 2; j < len(data); j++ {
			b := data[j]
			if b >= 0x40 && b <= 0x7e {
				end = j
				break
			}
		}
		if end < 0 {
			// Incomplete CSI sequence at the end of the buffer:
			// hold it for the next read.
			seqTail := data[start:]
			*hold = append([]byte{}, seqTail...)
			break
		}
		seq := data[start : end+1]
		term := seq[len(seq)-1]
		if term == 'u' {
			if legacy, ok := parseKittyCSIu(seq); ok {
				out = append(out, legacy...)
			} else {
				out = append(out, seq...)
			}
		} else if term == '~' && bytes.HasPrefix(seq, []byte("\x1b[27;")) {
			if legacy, ok := csiuLegacy(seq); ok {
				out = append(out, legacy...)
			} else {
				out = append(out, seq...)
			}
		} else {
			// Other CSI sequence (e.g. standard \x1b[A, \x1b[1;3A, \x1b[3~): pass through.
			out = append(out, seq...)
		}
		i = end + 1
	}
	return out
}

// parseKittyCSIu parses ESC[<codepoint>[:<alternate>...][;<mod>[:<event>]][;<text>]u
func parseKittyCSIu(seq []byte) ([]byte, bool) {
	if len(seq) < 3 || seq[0] != '\x1b' || seq[1] != '[' || seq[len(seq)-1] != 'u' {
		return nil, false
	}
	payload := string(seq[2 : len(seq)-1])
	parts := strings.Split(payload, ";")
	if len(parts) == 0 {
		return nil, false
	}

	keyPart := parts[0]
	subKeys := strings.Split(keyPart, ":")
	key, err := strconv.Atoi(subKeys[0])
	if err != nil {
		return nil, false
	}
	shiftedKey := 0
	if len(subKeys) > 1 {
		shiftedKey, _ = strconv.Atoi(subKeys[1])
	}

	mod := 1
	eventType := 1 // default: press
	if len(parts) > 1 && parts[1] != "" {
		subMods := strings.Split(parts[1], ":")
		m, err := strconv.Atoi(subMods[0])
		if err == nil {
			mod = m
		}
		if len(subMods) > 1 {
			ev, err := strconv.Atoi(subMods[1])
			if err == nil {
				eventType = ev
			}
		}
	}

	if eventType == 3 {
		// Key release: bubbletea only handles key down/repeat; drop release.
		return []byte{}, true
	}

	return formatLegacyKey(key, shiftedKey, mod)
}

// csiuLegacy converts one complete xterm modifyOtherKeys sequence to its legacy form.
func csiuLegacy(seq []byte) ([]byte, bool) {
	var mod, key int
	if _, err := scanSeq(seq, &mod, &key); err != nil {
		return nil, false
	}
	return formatLegacyKey(key, 0, mod)
}

// formatLegacyKey converts a key codepoint and modifier to bubbletea-parseable bytes.
// mod: 1 none, 2 shift, 3 alt, 4 shift+alt, 5 ctrl, 6 ctrl+shift,
// 7 ctrl+alt, 8 ctrl+shift+alt.
func formatLegacyKey(key, shiftedKey, mod int) ([]byte, bool) {
	switch {
	case key == 57373 || (key >= 65 && key <= 68): // arrows A-D or kitty functional Up
		letter := byte(key)
		if key == 57373 {
			letter = 'A'
		}
		switch mod {
		case 1:
			return []byte{'\x1b', '[', letter}, true
		case 2, 3, 4, 5, 6:
			// ESC[1;<mod><letter> — bubbletea parses these (1;2=shift,
			// 1;3=alt, 1;4=shift+alt, 1;5=ctrl, 1;6=ctrl+shift).
			return []byte{'\x1b', '[', '1', ';', byte('0' + mod), letter}, true
		}
	case key == 57374: // kitty functional Down
		if mod == 1 {
			return []byte{'\x1b', '[', 'B'}, true
		}
		return []byte{'\x1b', '[', '1', ';', byte('0' + mod), 'B'}, true
	case key == 57375: // kitty functional Right
		if mod == 1 {
			return []byte{'\x1b', '[', 'C'}, true
		}
		return []byte{'\x1b', '[', '1', ';', byte('0' + mod), 'C'}, true
	case key == 57376: // kitty functional Left
		if mod == 1 {
			return []byte{'\x1b', '[', 'D'}, true
		}
		return []byte{'\x1b', '[', '1', ';', byte('0' + mod), 'D'}, true
	case key == 57377 || key == 72: // home
		if mod == 1 {
			return []byte{'\x1b', '[', 'H'}, true
		}
		return []byte{'\x1b', '[', '1', ';', byte('0' + mod), 'H'}, true
	case key == 57378 || key == 70: // end
		if mod == 1 {
			return []byte{'\x1b', '[', 'F'}, true
		}
		return []byte{'\x1b', '[', '1', ';', byte('0' + mod), 'F'}, true
	case key == 13: // enter
		switch mod {
		case 1, 2:
			return []byte{'\r'}, true
		case 3, 4:
			return []byte{'\x1b', '\r'}, true // alt+enter
		}
	case key == 9: // tab
		switch mod {
		case 1:
			return []byte{'\t'}, true
		case 2:
			return []byte{'\x1b', '[', 'Z'}, true // shift+tab
		case 3, 4:
			return []byte{'\x1b', '\t'}, true
		}
	case key == 127 || key == 8: // backspace
		switch mod {
		case 1, 2:
			return []byte{0x7f}, true
		case 3, 4:
			return []byte{'\x1b', 0x7f}, true
		}
	case key == 27: // escape
		switch mod {
		case 1, 2:
			return []byte{'\x1b'}, true
		case 3, 4:
			return []byte{'\x1b', '\x1b'}, true // alt+esc
		}
	case key >= 32 && key <= 126: // printable
		switch mod {
		case 1:
			return []byte{byte(key)}, true // plain char
		case 2: // shift
			ch := byte(key)
			if shiftedKey > 0 && shiftedKey <= 126 {
				ch = byte(shiftedKey)
			} else if ch >= 'a' && ch <= 'z' {
				ch = ch - 'a' + 'A'
			}
			return []byte{ch}, true
		case 3: // alt
			return []byte{'\x1b', byte(key)}, true // alt+char
		case 4: // shift+alt
			ch := byte(key)
			if shiftedKey > 0 && shiftedKey <= 126 {
				ch = byte(shiftedKey)
			} else if ch >= 'a' && ch <= 'z' {
				ch = ch - 'a' + 'A'
			}
			return []byte{'\x1b', ch}, true // alt+shifted char
		case 5:
			if key >= 97 && key <= 122 { // ctrl+letter → control byte
				return []byte{byte(key - 96)}, true
			}
			if key >= 65 && key <= 90 {
				return []byte{byte(key - 64)}, true
			}
		case 7: // ctrl+alt
			if key >= 97 && key <= 122 {
				return []byte{'\x1b', byte(key - 96)}, true
			}
			if key >= 65 && key <= 90 {
				return []byte{'\x1b', byte(key - 64)}, true
			}
		}
	}
	return nil, false
}

// scanSeq parses ESC[27;<mod>;<key>~ (seq includes the '~').
func scanSeq(seq []byte, mod, key *int) (int, error) {
	if len(seq) < 9 || string(seq[:5]) != "\x1b[27;" {
		return 0, errBadCSIu
	}
	rest := seq[5:]
	*mod = 0
	for len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
		*mod = *mod*10 + int(rest[0]-'0')
		rest = rest[1:]
	}
	if len(rest) == 0 || rest[0] != ';' {
		return 0, errBadCSIu
	}
	rest = rest[1:]
	*key = 0
	for len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
		*key = *key*10 + int(rest[0]-'0')
		rest = rest[1:]
	}
	if len(rest) == 0 || rest[0] != '~' {
		return 0, errBadCSIu
	}
	return len(seq) - len(rest), nil
}

var errBadCSIu = errorString("bad CSI-u sequence")

type errorString string

func (e errorString) Error() string { return string(e) }

package main

import (
	"bytes"
	"os"
)

// keyboardInput wraps stdin and translates CSI-u (kitty keyboard
// protocol) escape sequences — ESC[27;<mod>;<key>~ — into the legacy
// escape forms bubbletea v1.3.10 parses (ESC[A for arrows, ESC+m for
// Alt+M, …). Terminals that emit CSI-u for modified keys (kitty,
// wezterm, Windows Terminal with the enhanced keyboard protocol
// enabled) otherwise deliver ↓ and Alt+letter as unknown sequences
// that bubbletea silently drops.
//
// It keeps the Fd() of the underlying stdin so the Windows console
// path (which reads input records directly, never the byte stream)
// still engages — the translation only ever applies to byte-based
// input (Unix ttys, SSH, WSL, ConPTY fallbacks).
type keyboardInput struct {
	inner   *os.File
	pending []byte // translated bytes not yet returned
	hold    []byte // incomplete CSI-u prefix waiting for its '~'
}

func (k *keyboardInput) Read(p []byte) (int, error) {
	if len(k.pending) > 0 {
		n := copy(p, k.pending)
		k.pending = k.pending[n:]
		return n, nil
	}
	buf := make([]byte, 256)
	n, err := k.inner.Read(buf)
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
func (k *keyboardInput) Fd() uintptr                 { return k.inner.Fd() }

// translateCSIu rewrites complete CSI-u sequences in data into legacy
// forms; a trailing incomplete sequence prefix is stored in *hold and
// prepended to the next chunk by the caller. All other bytes pass
// through untouched.
func translateCSIu(data []byte, hold *[]byte) []byte {
	out := make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		// Look for the CSI-u introducer ESC[27;
		idx := bytes.Index(data[i:], []byte("\x1b[27;"))
		if idx < 0 {
			out = append(out, data[i:]...)
			break
		}
		out = append(out, data[i:i+idx]...)
		seq := data[i+idx:]
		end := bytes.IndexByte(seq, '~')
		if end < 0 {
			// Incomplete: hold it for the next read.
			*hold = append([]byte{}, seq...)
			break
		}
		seq = seq[:end+1]
		if legacy, ok := csiuLegacy(seq); ok {
			out = append(out, legacy...)
		} else {
			out = append(out, seq...)
		}
		i += idx + len(seq)
	}
	return out
}

// csiuLegacy converts one complete CSI-u sequence to its legacy form.
// mod: 1 none, 2 shift, 3 alt, 4 shift+alt, 5 ctrl, 6 ctrl+shift,
// 7 ctrl+alt, 8 ctrl+shift+alt.
func csiuLegacy(seq []byte) ([]byte, bool) {
	var mod, key int
	if _, err := scanSeq(seq, &mod, &key); err != nil {
		return nil, false
	}
	switch {
	case key >= 65 && key <= 68: // arrows A-D
		switch mod {
		case 1:
			return []byte{'\x1b', '[', byte(key)}, true
		case 2, 3, 4, 5, 6:
			// ESC[1;<mod><letter> — bubbletea parses these (1;2=shift,
			// 1;3=alt, 1;4=shift+alt, 1;5=ctrl, 1;6=ctrl+shift).
			return []byte{'\x1b', '[', '1', ';', byte('0' + mod), byte(key)}, true
		default:
			return nil, false
		}
	case key == 72 || key == 70: // home, end
		if mod == 1 {
			return []byte{'\x1b', '[', byte(key)}, true
		}
		return []byte{'\x1b', '[', '1', ';', byte('0' + mod), byte(key)}, true
	case key == 13: // enter
		if mod == 1 {
			return []byte{'\r'}, true
		}
		if mod == 3 {
			return []byte{'\x1b', '\r'}, true // alt+enter
		}
	case key == 9: // tab
		switch mod {
		case 1:
			return []byte{'\t'}, true
		case 2:
			return []byte{'\x1b', '[', 'Z'}, true // shift+tab
		case 3:
			return []byte{'\x1b', '\t'}, true
		}
	case key == 127: // backspace
		if mod == 1 {
			return []byte{0x7f}, true
		}
		if mod == 3 {
			return []byte{'\x1b', 0x7f}, true
		}
	case key == 27: // escape
		if mod == 1 {
			return []byte{'\x1b'}, true
		}
		if mod == 3 {
			return []byte{'\x1b', '\x1b'}, true // alt+esc
		}
	case key >= 32 && key <= 126: // printable
		switch mod {
		case 1, 2:
			return []byte{byte(key)}, true // plain (or shifted) char
		case 3:
			return []byte{'\x1b', byte(key)}, true // alt+char
		case 5:
			if key >= 97 && key <= 122 { // ctrl+letter → control byte
				return []byte{byte(key - 96)}, true
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

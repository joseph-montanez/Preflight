package browser

import (
	"os"
	"path/filepath"
	"testing"
	_ "unsafe"

	tea "github.com/charmbracelet/bubbletea"
)

//go:linkname detectOneMsg github.com/charmbracelet/bubbletea.detectOneMsg
func detectOneMsg(b []byte) (int, tea.Msg)

// feedRealBytes runs the REAL bubbletea input parser over the byte
// stream, one keystroke at a time (as a terminal delivers them).
func feedRealBytes(t *testing.T, m tea.Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		remaining := []byte(k)
		for len(remaining) > 0 {
			n, msg := detectOneMsg(remaining)
			if n == 0 {
				t.Fatalf("parser stalled on %q", remaining)
			}
			remaining = remaining[n:]
			if km, ok := msg.(tea.KeyMsg); ok {
				t.Logf("parser -> %q (alt=%v runes=%q)", km.String(), km.Alt, string(km.Runes))
				updated, cmd := m.Update(km)
				m = settleTestCommands(updated, cmd)
			}
		}
	}
	return m.(Model)
}

// Exactly what a terminal sends for: "/" "a" "p" "i" ↓ ↓ Alt+M.
func TestRealBytesAltMMarksThirdResult(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "api"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "shared", "apicore"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "rapid"), 0o755)
	m := feedRealBytes(t, New(root, ""),
		"/", "a", "p", "i", "\x1b[B", "\x1b[B", "\x1bm")
	if !m.marked["rapid"] {
		t.Fatalf("marked = %v, want rapid (3rd result)", m.marked)
	}
}

func TestRealBytesAltShiftMMarksHighlighted(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "api"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "shared", "apicore"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "rapid"), 0o755)
	m := feedRealBytes(t, New(root, ""),
		"/", "a", "p", "i", "\x1b[B", "\x1b[B", "\x1bM") // note: uppercase M
	if !m.marked["rapid"] {
		t.Fatalf("Alt+Shift+M must mark the highlighted result; marked = %v, query = %q (M leaked into the query)", m.marked, m.input.Value())
	}
	if m.input.Value() != "api" {
		t.Fatalf("query = %q, want api (no leaked letter)", m.input.Value())
	}
}

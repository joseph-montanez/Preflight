package localbrowser

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// altKey builds an alt-modified letter key (ESC+letter), the reliable
// modifier form bubbletea parses.
func altKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Alt: true}
}

// editModel builds a browser parked in the mapped folder public/.
func editModel(t *testing.T) Model {
	t.Helper()
	m, root := newTestModel(t)
	m.editorCmd = "hx"
	m.current = filepath.Join(root, "public")
	m.load()
	return m
}

func TestAltEOpensFile(t *testing.T) {
	m, root := newTestModel(t)
	m.editorCmd = "hx"
	m.current = filepath.Join(root, "public")
	m.load()
	m = cursorTo(m, "index.html")
	updated, _ := m.Update(altKey("e"))
	mm := updated.(Model)
	if want := filepath.Join(root, "public", "index.html"); mm.EditRequest() != want {
		t.Fatalf("EditRequest = %q, want %q", mm.EditRequest(), want)
	}
}

func TestAltEOpensFolder(t *testing.T) {
	m := editModel(t)
	m = cursorTo(m, "img")
	updated, _ := m.Update(altKey("e"))
	mm := updated.(Model)
	if want := filepath.Join(m.localRoot, "public", "img"); mm.EditRequest() != want {
		t.Fatalf("EditRequest = %q, want %q", mm.EditRequest(), want)
	}
}

func TestAltEUnconfiguredHint(t *testing.T) {
	m, root := newTestModel(t)
	m.current = filepath.Join(root, "public")
	m.load()
	m = cursorTo(m, "index.html")
	updated, _ := m.Update(altKey("e"))
	mm := updated.(Model)
	if mm.EditRequest() != "" {
		t.Fatal("no editor configured: alt+e must not request an edit")
	}
	if !strings.Contains(mm.status, "editor") {
		t.Fatalf("status = %q, want editor hint", mm.status)
	}
}

func TestSearchAltMMarksResult(t *testing.T) {
	m, root := newTestModel(t)
	m.current = filepath.Join(root, "public")
	m.load()
	m = send(m, "/", "index")
	mm := m
	mm = send(mm, "down", "down") // cursor onto the result row
	updated, _ := mm.Update(altKey("m"))
	mm = updated.(Model)
	if !mm.marked[filepath.Join(root, "public", "index.html")] {
		t.Fatalf("alt+m in search must mark: %v", mm.marked)
	}
	if !mm.searchOn {
		t.Fatal("search should stay open after alt+m")
	}
	// Space is still query text while searching.
	m2 := editModel(t)
	m2 = send(m2, "/")
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	m2 = updated.(Model)
	if m2.search.Value() != " " {
		t.Fatalf("query = %q, want space preserved", m2.search.Value())
	}
}

func TestSearchAltEOpensResult(t *testing.T) {
	m := editModel(t)
	root := m.localRoot
	m = send(m, "/", "index")
	mm := m
	mm = send(mm, "down", "down")
	updated, _ := mm.Update(altKey("e"))
	mm = updated.(Model)
	if want := filepath.Join(root, "public", "index.html"); mm.EditRequest() != want {
		t.Fatalf("alt+e in search: EditRequest = %q, want %q", mm.EditRequest(), want)
	}
}

func TestMarkedPathsRoundTrip(t *testing.T) {
	m, root := newTestModel(t)
	m.current = filepath.Join(root, "public")
	m.load()
	m = cursorTo(m, "index.html")
	m = send(m, " ")
	paths := m.MarkedPaths()
	if len(paths) != 1 || paths[0] != filepath.Join(root, "public", "index.html") {
		t.Fatalf("MarkedPaths = %v", paths)
	}
	restored := New(m.localRoot, m.mappings, nil, "")
	restored.RestoreMarks(paths)
	if !restored.marked[paths[0]] {
		t.Fatal("RestoreMarks failed")
	}
}

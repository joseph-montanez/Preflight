package remotebrowser

import (
	"path/filepath"
	"strings"
	"testing"
)

// openBrowser builds a browser at the virtual root with the given
// editor command.
func openBrowser(editorCmd string) Model {
	return New(newFakeRemote(), testMappings(), filepath.FromSlash("/home/dev/code/api-backend"), editorCmd)
}

// navigate jumps straight to /var/www/api-backend and leaves the
// cursor on the given row (0 = "(select marked)", 1 = "..", then
// entries in dirs-first order: assets/, index.php, .env).
func navigate(t *testing.T, m Model, row int) Model {
	t.Helper()
	m.current = "/var/www/api-backend"
	m.load()
	for i := 0; i < row; i++ {
		m = sendKey(m, "down").(Model)
	}
	return m
}

func TestEditKeyOnRemoteFile(t *testing.T) {
	m := navigate(t, openBrowser("hx"), 4) // rows: assets, .env, index.php
	m = sendKey(m, "e").(Model)
	mm := m
	if mm.EditRequest() != "/var/www/api-backend/index.php" {
		t.Fatalf("EditRequest = %q", mm.EditRequest())
	}
}

func TestEditKeyOnFolderHints(t *testing.T) {
	m := navigate(t, openBrowser("hx"), 2) // assets/ (dir)
	m = sendKey(m, "e").(Model)
	mm := m
	if mm.EditRequest() != "" {
		t.Fatalf("dir edit must not request: %q", mm.EditRequest())
	}
	if !strings.Contains(mm.status, "file") {
		t.Fatalf("status = %q, want file hint", mm.status)
	}
	// Row 0 (current folder) and ".." also hint.
	m = navigate(t, openBrowser("hx"), 0)
	m = sendKey(m, "e").(Model)
	if m.EditRequest() != "" {
		t.Fatal("row 0 must not request an edit")
	}
}

func TestEditKeyUnconfiguredHints(t *testing.T) {
	m := navigate(t, openBrowser(""), 4) // index.php, but no editor configured
	m = sendKey(m, "e").(Model)
	mm := m
	if mm.EditRequest() != "" {
		t.Fatal("no editor configured: e must not request an edit")
	}
	if !strings.Contains(mm.status, "editor") {
		t.Fatalf("status = %q, want editor hint", mm.status)
	}
}

func TestEditKeyDisabledInPickDir(t *testing.T) {
	m := PickRemoteDir(newFakeRemote(), "/var/www/api-backend")
	m = sendKey(m, "down").(Model) // assets/
	m = sendKey(m, "down").(Model) // index.php
	m = sendKey(m, "e").(Model)
	mm := m
	if mm.EditRequest() != "" {
		t.Fatal("pickDir mode must not request an edit")
	}
	if !strings.Contains(mm.status, "destination") {
		t.Fatalf("status = %q, want destination hint", mm.status)
	}
}

func TestFilterAltMarksAndEdit(t *testing.T) {
	// "/" filter: type "ind" to narrow to index.php, alt+m marks it,
	// alt+e requests an edit on it. The cursor must sit on the result
	// row (row 0 marks the current folder, matching list semantics).
	m := navigate(t, openBrowser("hx"), 0)
	m = sendKey(m, "/").(Model)
	m = sendKey(m, "ind").(Model)
	mm := m
	if !mm.filterOn || len(mm.visible()) == 0 {
		t.Fatalf("filter not narrowed: filterOn=%v visible=%v", mm.filterOn, mm.visible())
	}
	mm = sendKey(mm, "down").(Model) // ..
	mm = sendKey(mm, "down").(Model) // index.php
	updated, _ := mm.Update(altKey("m"))
	mm = updated.(Model)
	if !mm.marked["/var/www/api-backend/index.php"] {
		t.Fatalf("alt+m in filter must mark: %v", mm.marked)
	}
	updated, _ = mm.Update(altKey("e"))
	mm = updated.(Model)
	if mm.EditRequest() != "/var/www/api-backend/index.php" {
		t.Fatalf("alt+e in filter: EditRequest = %q", mm.EditRequest())
	}
}

func TestFilterAltEDirHints(t *testing.T) {
	m := navigate(t, openBrowser("hx"), 0)
	m = sendKey(m, "/").(Model)
	m = sendKey(m, "asset").(Model)
	mm := m
	updated, _ := mm.Update(altKey("e"))
	mm = updated.(Model)
	if mm.EditRequest() != "" {
		t.Fatal("alt+e on a dir result must not request an edit")
	}
	if !strings.Contains(mm.status, "file") {
		t.Fatalf("status = %q, want file hint", mm.status)
	}
}

func TestFilterAltMDisabledInPickDir(t *testing.T) {
	m := PickRemoteDir(newFakeRemote(), "/var/www/api-backend")
	m = sendKey(m, "/").(Model)
	m = sendKey(m, "ind").(Model)
	mm := m
	updated, _ := mm.Update(altKey("m"))
	mm = updated.(Model)
	if len(mm.marked) != 0 {
		t.Fatalf("pickDir must not mark from filter: %v", mm.marked)
	}
}

func TestFilterSpaceStillTypesQuery(t *testing.T) {
	m := navigate(t, openBrowser("hx"), 0)
	m = sendKey(m, "/").(Model)
	m = sendKey(m, "in de").(Model) // spaces stay query text
	mm := m
	if !mm.filterOn || mm.input.Value() != "in de" {
		t.Fatalf("query = %q, want spaces preserved", mm.input.Value())
	}
}

func TestMarkedPathsRoundTrip(t *testing.T) {
	m := navigate(t, openBrowser("hx"), 4) // index.php
	m = sendKey(m, " ").(Model)
	mm := m
	paths := mm.MarkedPaths()
	if len(paths) != 1 || paths[0] != "/var/www/api-backend/index.php" {
		t.Fatalf("MarkedPaths = %v", paths)
	}
	restored := openBrowser("")
	restored.RestoreMarks(paths)
	if !restored.marked["/var/www/api-backend/index.php"] {
		t.Fatal("RestoreMarks failed")
	}
}

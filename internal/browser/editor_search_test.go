package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchFindsFiles(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "api", "cmd"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "api", "main.go"), []byte("x"), 0o644)
	m := sendKey(New(root, ""), "/")
	m = sendKey(m, "main")
	mm := m.(Model)
	if len(mm.results) == 0 {
		t.Fatal("expected search to find files")
	}
	if mm.results[0].path != "api/main.go" || mm.results[0].dir {
		t.Fatalf("results[0] = %+v, want api/main.go file", mm.results[0])
	}
	// Dirs rank before files within the same tier.
	m = sendKey(New(root, ""), "/")
	m = sendKey(m, "api")
	mm = m.(Model)
	if len(mm.results) < 2 || !mm.results[0].dir || mm.results[0].path != "api" {
		t.Fatalf("folder should rank first: %+v", mm.results)
	}
}

func TestSearchIndexLoadsAsynchronously(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "kiosks", "public_html", "actions"), 0o755)

	updated, cmd := New(root, "").Update(key("/"))
	mm := updated.(Model)
	if cmd == nil {
		t.Fatal("opening search must return an asynchronous indexing command")
	}
	if mm.dirIndex != nil || mm.fileIndex != nil {
		t.Fatal("opening search must not index the project inside Update")
	}
	if !mm.inputMode {
		t.Fatal("search input should be usable while indexing")
	}

	updated, _ = mm.Update(key("k/p/a"))
	mm = updated.(Model)
	if mm.input.Value() != "k/p/a" {
		t.Fatalf("query while indexing = %q, want k/p/a", mm.input.Value())
	}

	updated, _ = mm.Update(cmd())
	mm = updated.(Model)
	if len(mm.results) == 0 || mm.results[0].path != "kiosks/public_html/actions" {
		t.Fatalf("results after indexing = %+v, want abbreviated folder match", mm.results)
	}
}

func TestSearchOrderedExtensionAndContentsFilters(t *testing.T) {
	root := t.TempDir()
	actions := filepath.Join(root, "kiosks", "public_html", "actions")
	_ = os.MkdirAll(filepath.Join(actions, "admin"), 0o755)
	_ = os.WriteFile(filepath.Join(actions, "machine.php"), []byte("$group = 'machines_demo';"), 0o644)
	_ = os.WriteFile(filepath.Join(actions, "admin", "other.php"), []byte("no match"), 0o644)
	_ = os.WriteFile(filepath.Join(actions, "machine.txt"), []byte("machines_demo"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "transactions.php"), []byte("machines_demo"), 0o644)

	m := sendKey(New(root, ""), "/")
	m = sendKey(m, "actions/ | %ext *.php | %contents: machines_demo")
	mm := m.(Model)
	if len(mm.results) != 1 || mm.results[0].path != "kiosks/public_html/actions/machine.php" || mm.results[0].dir {
		t.Fatalf("filtered results = %+v, want only machine.php", mm.results)
	}
}

func TestSearchExplicitStageAfterTypeFilter(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "kiosks", "public_html", "actions"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "actions.php"), []byte("x"), 0o644)

	m := sendKey(New(root, ""), "/")
	m = sendKey(m, "%type: folder | %s actions")
	mm := m.(Model)
	if len(mm.results) == 0 {
		t.Fatal("expected an actions folder result")
	}
	for _, result := range mm.results {
		if !result.dir {
			t.Fatalf("type filter returned file %+v", result)
		}
	}
}

func TestSearchAltMMarksResult(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644)
	m := sendKey(New(root, ""), "/")
	m = sendKey(m, "notes")
	mm := m.(Model)
	// alt+m marks the highlighted result and stays in the search.
	updated, _ := mm.Update(altKey("m"))
	mm = updated.(Model)
	if !mm.inputMode {
		t.Fatal("search should stay open after alt+m")
	}
	if !mm.marked["notes.txt"] {
		t.Fatalf("notes.txt not marked: %v", mm.marked)
	}
	if !strings.Contains(mm.status, "marked notes.txt") {
		t.Fatalf("status = %q", mm.status)
	}
	// The popup renders a check for the marked row.
	mm.width, mm.height = 80, 24
	if !strings.Contains(mm.View(), "✓ notes.txt") {
		t.Fatalf("popup should render check: %s", mm.View())
	}
	// alt+m again unmarks.
	updated, _ = mm.Update(altKey("m"))
	mm = updated.(Model)
	if mm.marked["notes.txt"] {
		t.Fatal("alt+m should toggle the mark off")
	}
}

func TestSearchAltMMarksFolderWithoutDescend(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "kiosks", "public_html", "actions"), 0o755)
	m := sendKey(New(root, ""), "/")
	for _, r := range []string{"a", "c", "t", "i", "o", "n", "s"} {
		m = sendKey(m, r)
	}
	mm := m.(Model)
	if len(mm.results) == 0 || mm.results[0].path != "kiosks/public_html/actions" {
		t.Fatalf("results = %+v, want the actions folder", mm.results)
	}
	updated, _ := mm.Update(altKey("m"))
	mm = updated.(Model)
	if !mm.marked["kiosks/public_html/actions"] {
		t.Fatalf("folder should be marked straight from search: %v", mm.marked)
	}
	if !mm.inputMode {
		t.Fatal("should still be searching after marking")
	}
	// Esc closes the popup, a second Esc exits the search entirely.
	mm = sendKey(mm, "esc").(Model)
	mm = sendKey(mm, "esc").(Model)
	if mm.inputMode {
		t.Fatal("esc should close the search")
	}
	mm.width, mm.height = 80, 24
	if !strings.Contains(mm.View(), "Selected: kiosks/public_html/actions") {
		t.Fatalf("status line should list the selection: %s", mm.View())
	}
	// The check appears in the marked folder's own listing (list-view
	// semantics: marks render on the entry row, not on its ancestors).
	mm = sendKey(mm, "down").(Model)  // kiosks/
	mm = sendKey(mm, "enter").(Model) // descend kiosks
	mm = sendKey(mm, "down").(Model)  // ..
	mm = sendKey(mm, "down").(Model)  // public_html/
	mm = sendKey(mm, "enter").(Model) // descend public_html
	mm = sendKey(mm, "down").(Model)  // ..
	mm = sendKey(mm, "down").(Model)  // actions/
	mm = sendKey(mm, "enter").(Model) // descend actions (marked)
	if !strings.Contains(mm.View(), "✓ (select this folder)") {
		t.Fatalf("row 0 should render the mark after descending: %s", mm.View())
	}
}

func TestSearchAltMKeepsSpaceTyping(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "my file.txt"), []byte("x"), 0o644)
	m := sendKey(New(root, ""), "/")
	m = sendKey(m, "my file")
	mm := m.(Model)
	if mm.input.Value() != "my file" {
		t.Fatalf("query = %q, want space preserved in query", mm.input.Value())
	}
	if len(mm.results) != 1 || mm.results[0].path != "my file.txt" {
		t.Fatalf("results = %+v, want [my file.txt]", mm.results)
	}
	updated, _ := mm.Update(altKey("m"))
	mm = updated.(Model)
	if !mm.marked["my file.txt"] {
		t.Fatalf("mark failed: %v", mm.marked)
	}
}

func TestSearchAltMDisabledInPickDir(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "dest"), 0o755)
	m := sendKey(PickDir(root), "/")
	m = sendKey(m, "dest")
	mm := m.(Model)
	if len(mm.results) == 0 {
		t.Fatal("expected results")
	}
	updated, _ := mm.Update(altKey("m"))
	mm = updated.(Model)
	if len(mm.marked) != 0 {
		t.Fatalf("pickDir must not mark: %v", mm.marked)
	}
	if !strings.Contains(mm.status, "destination") {
		t.Fatalf("status = %q, want destination hint", mm.status)
	}
}

func TestSearchJKTypeInQueryAndAltMMarksHighlighted(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "kiosks"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "kiosks2"), 0o755)
	m := sendKey(New(root, ""), "/")
	// j/k are query text in the popup (filenames contain them): the
	// query must receive the letters, not the highlight.
	for _, r := range []string{"k", "i", "o", "s", "k"} {
		m = sendKey(m, r)
	}
	mm := m.(Model)
	if mm.input.Value() != "kiosk" {
		t.Fatalf("query = %q, want kiosk (j/k typed)", mm.input.Value())
	}
	if len(mm.results) != 2 {
		t.Fatalf("results = %v, want [kiosks kiosks2]", mm.results)
	}
	// Arrows move the highlight; alt+m marks exactly the highlighted row.
	m = sendKey(mm, "down")
	mm = m.(Model)
	if mm.resultAt != 1 {
		t.Fatalf("resultAt = %d, want 1 after down", mm.resultAt)
	}
	updated, _ := mm.Update(altKey("m"))
	mm = updated.(Model)
	if !mm.marked[mm.results[1].path] {
		t.Fatalf("alt+m must mark the highlighted result %q, got %v", mm.results[1].path, mm.marked)
	}
	if mm.marked[mm.results[0].path] {
		t.Fatalf("first result must not be marked: %v", mm.marked)
	}
}

func TestSearchAltMMarksTabCandidate(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "web"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "web2"), 0o755)
	m := sendKey(New(root, ""), "/")
	m = sendKey(m, "w")
	mm := m.(Model)
	mm = sendKey(mm, "tab").(Model) // single match "web/" fills the input
	if mm.input.Value() != "web" {
		t.Fatalf("input = %q, want web filled", mm.input.Value())
	}
	mm = sendKey(mm, "tab").(Model) // shared prefix: completion popup opens
	if mm.cands == nil || len(mm.cands) != 2 {
		t.Fatalf("cands = %v, want [web/ web2/]", mm.cands)
	}
	mm = sendKey(mm, "down").(Model) // move the candidate highlight
	if mm.candAt != 1 {
		t.Fatalf("candAt = %d, want 1", mm.candAt)
	}
	// The mark must land on the highlighted candidate, whatever the
	// platform-dependent sort order of the completion list is.
	highlighted := strings.TrimSuffix(mm.cands[mm.candAt], "/")
	other := strings.TrimSuffix(mm.cands[1-mm.candAt], "/")
	updated, _ := mm.Update(altKey("m"))
	mm = updated.(Model)
	if !mm.marked[highlighted] {
		t.Fatalf("alt+m must mark the highlighted candidate %q, got %v", highlighted, mm.marked)
	}
	if mm.marked[other] {
		t.Fatalf("the other candidate must not be marked: %v", mm.marked)
	}
	// The completion popup renders a check on the marked candidate.
	mm.width, mm.height = 80, 24
	if !strings.Contains(mm.View(), "✓ "+highlighted+"/") {
		t.Fatalf("completion popup should render the check: %s", mm.View())
	}
}

func TestEditorKeyOnFile(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, "hx"), "down") // a
	m = sendKey(m, "down")                // c
	m = sendKey(m, "down")                // file.txt
	m = sendKey(m, "e")
	mm := m.(Model)
	if !mm.EditorMode() {
		t.Fatal("e should request the editor on the file under the cursor")
	}
	if want := filepath.Join(root, "file.txt"); mm.EditTarget() != want {
		t.Fatalf("EditTarget = %q, want %q", mm.EditTarget(), want)
	}
}

func TestEditorKeyOnFolder(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, "hx"), "down") // a
	m = sendKey(m, "e")
	mm := m.(Model)
	if want := filepath.Join(root, "a"); !mm.EditorMode() || mm.EditTarget() != want {
		t.Fatalf("editor mode = %v target = %q, want folder %q", mm.EditorMode(), mm.EditTarget(), want)
	}
}

func TestEditorKeyRowZeroAndDots(t *testing.T) {
	root := tree(t)
	// Row 0 (cursor on root): edit the current folder.
	mm := sendKey(New(root, "hx"), "e").(Model)
	if want := root; !mm.EditorMode() || mm.EditTarget() != want {
		t.Fatalf("row 0 target = %q, want %q", mm.EditTarget(), want)
	}
	// Descend into a/b, cursor on "..": edit the parent folder.
	m := sendKey(New(root, "hx"), "down") // a
	m = sendKey(m, "enter")               // descend into a
	m = sendKey(m, "down")                // ".." row
	m = sendKey(m, "e")
	mm = m.(Model)
	if want := root; !mm.EditorMode() || mm.EditTarget() != want {
		t.Fatalf(".. target = %q, want %q", mm.EditTarget(), want)
	}
}

func TestEditorKeyDisabledInPickDir(t *testing.T) {
	m := sendKey(PickDir(tree(t)), "e")
	mm := m.(Model)
	if mm.EditorMode() {
		t.Fatal("e must be disabled in pickDir mode")
	}
}

func TestEditorKeyUnconfiguredHint(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, ""), "down") // a
	m = sendKey(m, "down")              // c
	m = sendKey(m, "down")              // file.txt
	m = sendKey(m, "e")
	mm := m.(Model)
	if mm.EditorMode() {
		t.Fatal("no editor configured: e must not quit")
	}
	if !strings.Contains(mm.status, "editor") {
		t.Fatalf("status = %q, want editor hint", mm.status)
	}
}

func TestEditorAltEOnSearchResult(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "api"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "api", "main.go"), []byte("x"), 0o644)
	// File result.
	m := sendKey(New(root, "hx"), "/")
	m = sendKey(m, "main")
	mm := m.(Model)
	updated, _ := mm.Update(altKey("e"))
	mm = updated.(Model)
	if want := filepath.Join(root, "api", "main.go"); !mm.EditorMode() || mm.EditTarget() != want {
		t.Fatalf("alt+e file: mode=%v target=%q, want %q", mm.EditorMode(), mm.EditTarget(), want)
	}
	// Folder result.
	m = sendKey(New(root, "hx"), "/")
	m = sendKey(m, "api")
	mm = m.(Model)
	updated, _ = mm.Update(altKey("e"))
	mm = updated.(Model)
	if want := filepath.Join(root, "api"); !mm.EditorMode() || mm.EditTarget() != want {
		t.Fatalf("alt+e folder: mode=%v target=%q, want %q", mm.EditorMode(), mm.EditTarget(), want)
	}
	// Unconfigured editor: hint, no quit.
	m = sendKey(New(root, ""), "/")
	m = sendKey(m, "main")
	mm = m.(Model)
	updated, _ = mm.Update(altKey("e"))
	mm = updated.(Model)
	if mm.EditorMode() {
		t.Fatal("alt+e must not quit without a configured editor")
	}
	if !strings.Contains(mm.status, "editor") {
		t.Fatalf("status = %q, want editor hint", mm.status)
	}
}

func TestEditorEnterOnFileResultHintsNotJumps(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644)
	m := sendKey(New(root, ""), "/")
	m = sendKey(m, "notes")
	mm := m.(Model)
	mm = sendKey(mm, "enter").(Model)
	if !mm.inputMode {
		t.Fatal("enter on a file result must stay in search")
	}
	if !strings.Contains(mm.status, "file") {
		t.Fatalf("status = %q, want file hint", mm.status)
	}
}

func TestMarkedPathsRoundTrip(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, ""), "down") // a
	m = sendKey(m, " ")                 // mark a
	mm := m.(Model)
	paths := mm.MarkedPaths()
	if len(paths) != 1 || paths[0] != "a" {
		t.Fatalf("MarkedPaths = %v, want [a]", paths)
	}
	restored := New(root, "")
	restored.RestoreMarks(paths)
	if !restored.marked["a"] {
		t.Fatalf("RestoreMarks failed: %v", restored.marked)
	}
}

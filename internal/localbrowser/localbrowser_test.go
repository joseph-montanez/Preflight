package localbrowser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"preflight/internal/config"
	ignoreengine "preflight/internal/ignore"

	tea "github.com/charmbracelet/bubbletea"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// send applies a key and a window size (for View) to the model.
func send(m Model, keys ...string) Model {
	var updated tea.Model = m
	for _, k := range keys {
		updated, _ = updated.Update(key(k))
	}
	updated, _ = updated.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	return updated.(Model)
}

// cursorTo moves the cursor to the row of the entry with the given name
// in the current listing (row = index + 2, after "(select marked)" and
// "..").
func cursorTo(m Model, name string) Model {
	for i, e := range m.entries {
		if e.name == name {
			for m.cursor < i+2 {
				m = send(m, "down")
			}
			return m
		}
	}
	return m
}

// newTestModel builds a local browser over a temp project with a
// mapped folder "public" (→ /var/www/public), an unmapped folder
// "scripts", and an exclusion on "dist/".
func newTestModel(t *testing.T) (Model, string) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"public/img", "public/dist", "scripts", "dist"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"public/index.html", "public/img/logo.png", "public/dist/bundle.js", "scripts/deploy.sh", "dist/out.js", "README.md"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mappings := []config.PathMapping{{LocalPath: "public", RemotePath: "/var/www/public"}}
	ignores := ignoreengine.NewExclusionsOnly([]string{"dist/"})
	return New(root, mappings, ignores, ""), root
}

func TestVirtualRootListsMappings(t *testing.T) {
	m, _ := newTestModel(t)
	if !m.atRoot() {
		t.Fatal("model should start at the virtual root")
	}
	if len(m.entries) != 1 || m.entries[0].name != "public" {
		t.Fatalf("virtual root entries = %+v, want [public]", m.entries)
	}
	view := send(m).View()
	if !strings.Contains(view, "public/  →  /var/www/public/") {
		t.Fatalf("virtual root should label the mapping with its remote target:\n%s", view)
	}
	if !strings.Contains(view, "[E] exclusions OFF") {
		t.Fatalf("default filter state should be OFF:\n%s", view)
	}
}

func TestDescendIntoMappingAndMarkFile(t *testing.T) {
	m, root := newTestModel(t)
	// Enter on the mapping root descends into public/.
	m = cursorTo(m, "public")
	m = send(m, "enter")
	if m.atRoot() || m.current != filepath.Join(root, "public") {
		t.Fatalf("current = %q, want %q", m.current, filepath.Join(root, "public"))
	}
	for _, e := range m.entries {
		if e.name == "dist" && !e.excluded {
			t.Fatal("dist should be marked excluded")
		}
	}
	// Mark index.html (a file), then finish.
	m = cursorTo(m, "index.html")
	m = send(m, " ")
	if !m.marked[filepath.Join(root, "public/index.html")] {
		t.Fatal("index.html should be marked")
	}
	m = send(m, "s")
	roots, filterOn, ok := m.Selected()
	if !ok || len(roots) != 1 {
		t.Fatalf("Selected = %+v ok=%v, want 1 root", roots, ok)
	}
	if filterOn {
		t.Fatal("filter should default OFF")
	}
	if roots[0].RemotePath != "/var/www/public/index.html" {
		t.Fatalf("remote = %q, want /var/www/public/index.html", roots[0].RemotePath)
	}
}

func TestUnmappedRoot(t *testing.T) {
	m, root := newTestModel(t)
	// Left from the virtual root → project root; descend into scripts/.
	m = send(m, "left")
	if m.current != root {
		t.Fatalf(".. from virtual root should go to the project root, got %q", m.current)
	}
	m = cursorTo(m, "scripts")
	m = send(m, "enter") // descend into scripts/
	if m.current != filepath.Join(root, "scripts") {
		t.Fatalf("current = %q, want scripts dir", m.current)
	}
	// Mark the deploy.sh file (row 2, only entry), then finish.
	m = cursorTo(m, "deploy.sh")
	m = send(m, " ")
	m = send(m, "s")
	roots, _, ok := m.Selected()
	if !ok || len(roots) != 1 {
		t.Fatalf("Selected = %+v ok=%v", roots, ok)
	}
	if roots[0].RemotePath != "" {
		t.Fatalf("unmapped root should have empty RemotePath, got %q", roots[0].RemotePath)
	}
	if roots[0].LocalPath != filepath.Join(root, "scripts/deploy.sh") {
		t.Fatalf("local = %q", roots[0].LocalPath)
	}
}

func TestToggleExclusions(t *testing.T) {
	m, _ := newTestModel(t)
	if m.filterOn {
		t.Fatal("filter should start OFF")
	}
	m = send(m, "e")
	if !m.filterOn {
		t.Fatal("e should turn the filter ON")
	}
	if !strings.Contains(m.View(), "[E] exclusions ON") {
		t.Fatal("view should reflect the ON state")
	}
	m = send(m, "e")
	if m.filterOn {
		t.Fatal("e should turn the filter OFF again")
	}
}

func TestDimmedExcludedEntry(t *testing.T) {
	m, root := newTestModel(t)
	m = cursorTo(m, "public")
	m = send(m, "enter") // into public/
	view := m.View()
	if !strings.Contains(view, "⊘ dist") {
		t.Fatalf("dist should render dimmed with ⊘:\n%s", view)
	}
	// The excluded dir is still selectable — marking is never blocked.
	m = cursorTo(m, "dist")
	m = send(m, " ")
	if !m.marked[filepath.Join(root, "public/dist")] {
		t.Fatal("excluded entries must still be markable")
	}
}

func TestMarkWholeProjectRootMapping(t *testing.T) {
	root := t.TempDir()
	mappings := []config.PathMapping{{RemotePath: "/srv/app"}} // root mapping
	m := New(root, mappings, nil, "")
	if len(m.entries) != 1 || m.entries[0].name != "<project root>" {
		t.Fatalf("virtual root entries = %+v", m.entries)
	}
	m = cursorTo(m, "<project root>")
	m = send(m, " ") // mark the root mapping entry
	m = send(m, "s")
	roots, _, ok := m.Selected()
	if !ok || len(roots) != 1 {
		t.Fatalf("Selected = %+v ok=%v", roots, ok)
	}
	if roots[0].LocalPath != root || roots[0].RemotePath != "/srv/app" {
		t.Fatalf("root = %+v, want local %q remote /srv/app", roots[0], root)
	}
}

func TestFinishWithoutMarks(t *testing.T) {
	m, _ := newTestModel(t)
	m.Finish()
	if m.chosen {
		t.Fatal("finish with no marks must not confirm")
	}
	if !strings.Contains(m.status, "mark at least one") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestSearchNarrowsCurrentFolder(t *testing.T) {
	m, _ := newTestModel(t)
	m.current = filepath.Join(m.localRoot, "public")
	m.load()
	m = send(m, "/", "i", "n", "d")
	if !m.searchOn {
		t.Fatal("/ should open the name filter")
	}
	if m.rowCount() != 3 { // [select], .., index.html
		t.Fatalf("rowCount = %d, want 3 with only index.html matching", m.rowCount())
	}
	view := m.View()
	if !strings.Contains(view, "index.html") || strings.Contains(view, "img") {
		t.Fatalf("filtered view wrong: %q", view)
	}
	m = send(m, "down", "down", "enter") // .. then index.html, Enter marks it
	if !m.marked[filepath.Join(m.localRoot, "public", "index.html")] {
		t.Fatal("Enter on the filtered file should toggle its mark")
	}
}

func TestSearchMatchesDirsAndClearsOnDescend(t *testing.T) {
	m, _ := newTestModel(t)
	m.current = filepath.Join(m.localRoot, "public")
	m.load()
	m = send(m, "/", "i", "m", "g")
	if m.rowCount() != 3 { // [select], .., img/
		t.Fatalf("rowCount = %d, want 3", m.rowCount())
	}
	m = send(m, "down", "down", "enter")
	if m.current != filepath.Join(m.localRoot, "public", "img") {
		t.Fatalf("current = %q, want public/img", m.current)
	}
	if m.searchOn {
		t.Fatal("descending should clear the filter")
	}
}

func TestSearchEscRestoresListing(t *testing.T) {
	m, _ := newTestModel(t)
	m.current = filepath.Join(m.localRoot, "public")
	m.load()
	m = send(m, "/", "i", "n", "d")
	if m.rowCount() != 3 {
		t.Fatalf("filtered rowCount = %d, want 3", m.rowCount())
	}
	m = send(m, "esc")
	if m.searchOn {
		t.Fatal("esc should close the filter")
	}
	if m.rowCount() != 5 { // [select], .., dist, img, index.html
		t.Fatalf("rowCount = %d, want the full 5 rows back", m.rowCount())
	}
}

func TestSearchNoMatchesKeepsFlowSafe(t *testing.T) {
	m, _ := newTestModel(t)
	m.current = filepath.Join(m.localRoot, "public")
	m.load()
	m = send(m, "/", "z", "z")
	if m.rowCount() != 2 {
		t.Fatalf("rowCount = %d, want just [select] and ..", m.rowCount())
	}
	if !strings.Contains(m.View(), "no matches") {
		t.Fatalf("view should report no matches: %q", m.View())
	}
	m = send(m, "enter") // row 0: finish with nothing marked
	if m.chosen {
		t.Fatal("enter with no matches must not finish the flow")
	}
	if !strings.Contains(m.status, "mark at least one") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestSearchQTypesInsteadOfQuitting(t *testing.T) {
	m, _ := newTestModel(t)
	m.current = filepath.Join(m.localRoot, "public")
	m.load()
	m = send(m, "/", "q")
	if !m.searchOn || m.search.Value() != "q" {
		t.Fatalf("q should type into the filter (searchOn=%v, value=%q)", m.searchOn, m.search.Value())
	}
	if m.rowCount() != 2 {
		t.Fatalf("rowCount = %d, want 2", m.rowCount())
	}
}

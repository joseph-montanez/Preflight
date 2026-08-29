package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func tree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "c"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644)
	return root
}

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
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// altKey builds an alt-modified letter key (ESC+letter), the reliable
// modifier form bubbletea parses.
func altKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Alt: true}
}

func sendKey(m tea.Model, s string) tea.Model {
	updated, cmd := m.Update(key(s))
	return settleTestCommands(updated, cmd)
}

func settleTestCommands(updated tea.Model, cmd tea.Cmd) tea.Model {
	for cmd != nil {
		mm, ok := updated.(Model)
		if !ok || (!mm.indexing && !mm.searchPending && !mm.searching) {
			break
		}
		updated, cmd = updated.Update(cmd())
	}
	return updated
}

func TestSelectRoot(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, ""), "enter")
	scope, ok := m.(Model).Selected()
	if !ok || len(scope) != 0 {
		t.Fatalf("Selected() = (%v, %v), want empty list (full project), true", scope, ok)
	}
}

func TestDescendAndSelectSubfolder(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, ""), "down") // a/
	m = sendKey(m, "enter")             // descend
	if got := m.(Model).current; got != filepath.Join(root, "a") {
		t.Fatalf("current = %q, want %q", got, filepath.Join(root, "a"))
	}
	m = sendKey(m, "s") // select current folder
	scope, ok := m.(Model).Selected()
	if !ok || len(scope) != 1 || scope[0] != "a" {
		t.Fatalf("Selected() = (%v, %v), want ([a], true)", scope, ok)
	}
}

func TestSelectNestedFolder(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, ""), "down") // a/
	m = sendKey(m, "enter")             // into a: rows = [select, .., b/]
	m = sendKey(m, "down")              // ..
	m = sendKey(m, "down")              // b/
	m = sendKey(m, "enter")             // into a/b
	m = sendKey(m, "s")
	scope, ok := m.(Model).Selected()
	if !ok || len(scope) != 1 || scope[0] != "a/b" {
		t.Fatalf("Selected() = (%v, %v), want ([a/b], true)", scope, ok)
	}
}

func TestUpViaBackspace(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, ""), "down") // a/
	m = sendKey(m, "enter")             // into a
	m = sendKey(m, "left")              // back up
	if got := m.(Model).current; got != root {
		t.Fatalf("current = %q, want root %q", got, root)
	}
}

func TestUpViaDotsRow(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, ""), "down") // a/
	m = sendKey(m, "enter")             // into a
	m = sendKey(m, "down")              // .. row
	m = sendKey(m, "enter")             // up
	if got := m.(Model).current; got != root {
		t.Fatalf("current = %q, want root %q", got, root)
	}
	if m.(Model).canUp() {
		t.Fatal("canUp should be false at the root")
	}
}

func TestQuitWithoutSelection(t *testing.T) {
	m := sendKey(New(tree(t), ""), "q")
	if _, ok := m.(Model).Selected(); ok {
		t.Fatal("expected no selection after quit")
	}
}

func TestEnterOnFileMarksIt(t *testing.T) {
	root := tree(t)
	m := New(root, "")
	// rows at root: [select], a/, c/, file.txt → cursor 3 = file.txt
	mm := m
	mm.cursor = 3
	got := sendKey(mm, "enter").(Model)
	if got.chosen {
		t.Fatal("enter on a file must not finish the selection")
	}
	if !got.marked["file.txt"] {
		t.Fatalf("file.txt not marked: %v", got.marked)
	}
	got = sendKey(got, "s").(Model)
	scope, ok := got.Selected()
	if !ok || len(scope) != 1 || scope[0] != "file.txt" {
		t.Fatalf("Selected() = (%v, %v), want ([file.txt], true)", scope, ok)
	}
}

func TestDirFirstSorting(t *testing.T) {
	m := New(tree(t), "")
	names := make([]string, 0, len(m.entries))
	for _, e := range m.entries {
		names = append(names, e.name)
	}
	want := []string{"a", "c", "file.txt"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("entries = %v, want %v", names, want)
		}
	}
}

func TestPathInputJump(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, ""), "/")
	m = sendKey(m, "a")
	m = sendKey(m, "/")
	m = sendKey(m, "b")
	m = sendKey(m, "enter")
	mm := m.(Model)
	if mm.inputMode {
		t.Fatal("input mode should close after jump")
	}
	if mm.current != filepath.Join(root, "a", "b") {
		t.Fatalf("current = %q, want %q", mm.current, filepath.Join(root, "a", "b"))
	}
}

func TestPathInputSingleTabComplete(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, ""), "/")
	m = sendKey(m, "c")
	m = sendKey(m, "tab") // single match c/ auto-accepted
	mm := m.(Model)
	if mm.input.Value() != "c/" {
		t.Fatalf("value = %q, want c/", mm.input.Value())
	}
	m = sendKey(mm, "enter")
	if m.(Model).current != filepath.Join(root, "c") {
		t.Fatal("jump after completion failed")
	}
}

func TestPathInputPopupCycle(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "api-backend"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "api-frontend"), 0o755)
	m := sendKey(New(root, ""), "/")
	m = sendKey(m, "api-")
	m = sendKey(m, "tab")
	mm := m.(Model)
	if len(mm.cands) != 2 {
		t.Fatalf("cands = %v, want 2", mm.cands)
	}
	m = sendKey(mm, "tab") // cycle to the second candidate
	mm = m.(Model)
	if mm.candAt != 1 {
		t.Fatalf("candAt = %d, want 1", mm.candAt)
	}
	m = sendKey(mm, "enter") // accept candidate
	mm = m.(Model)
	if mm.input.Value() != "api-frontend/" {
		t.Fatalf("value = %q, want api-frontend/", mm.input.Value())
	}
	if mm.cands != nil {
		t.Fatal("popup should close after accept")
	}
	m = sendKey(mm, "enter") // jump
	if m.(Model).current != filepath.Join(root, "api-frontend") {
		t.Fatal("jump after popup accept failed")
	}
}

func TestPathInputInvalidStaysInInput(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, ""), "/")
	m = sendKey(m, "nope")
	m = sendKey(m, "enter")
	mm := m.(Model)
	if !mm.inputMode {
		t.Fatal("should stay in input mode on invalid path")
	}
	if !strings.Contains(mm.status, "no such folder") {
		t.Fatalf("status = %q, want no-such-folder hint", mm.status)
	}
	m = sendKey(mm, "esc")
	if m.(Model).inputMode {
		t.Fatal("esc should exit input mode")
	}
}

func TestPathInputEscLeavesFolder(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, ""), "/")
	m = sendKey(m, "a")
	m = sendKey(m, "esc") // first esc closes the search popup
	mm := m.(Model)
	if !mm.inputMode || mm.results != nil {
		t.Fatalf("expected popup closed, still in input: inputMode=%v results=%v", mm.inputMode, mm.results)
	}
	m = sendKey(mm, "esc") // second esc leaves input mode
	mm = m.(Model)
	if mm.inputMode {
		t.Fatal("esc should exit input mode")
	}
	if mm.current != root {
		t.Fatal("esc should not change the folder")
	}
}

func TestPathInputRootWithSlash(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, ""), "/")
	m = sendKey(m, "tab") // empty input + tab lists root children
	mm := m.(Model)
	if len(mm.cands) == 0 {
		t.Fatal("expected root children as candidates")
	}
	if mm.cands[0] != "a/" {
		t.Fatalf("first candidate = %q, want a/", mm.cands[0])
	}
}

func TestPathInputCommonPrefixFill(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "api-backend"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "api-frontend"), 0o755)
	m := sendKey(New(root, ""), "/")
	m = sendKey(m, "api")
	m = sendKey(m, "tab")
	mm := m.(Model)
	if mm.cands != nil {
		t.Fatalf("expected common-prefix fill, got popup %v", mm.cands)
	}
	if mm.input.Value() != "api-" {
		t.Fatalf("value = %q, want api- (common prefix)", mm.input.Value())
	}
	if mm.input.Position() != len("api-") {
		t.Fatalf("cursor at %d, want %d (end of value)", mm.input.Position(), len("api-"))
	}
}

func TestPathInputSearchResults(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "api", "cmd"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "web", "src"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "shared", "apicore"), 0o755)
	m := sendKey(New(root, ""), "/")
	// type a partial folder name, no path prefix
	m = sendKey(m, "api")
	mm := m.(Model)
	if len(mm.results) == 0 {
		t.Fatal("expected live search results for 'api'")
	}
	if mm.results[0].path != "api" {
		t.Fatalf("first result = %q, want api", mm.results[0].path)
	}
	found := false
	for _, r := range mm.results {
		if r.path == "shared/apicore" {
			found = true
		}
	}
	if !found {
		t.Fatalf("deep match shared/apicore missing from %v", mm.results)
	}
	// down arrow picks the second result, enter jumps to it
	m = sendKey(mm, "down")
	mm = m.(Model)
	if mm.resultAt != 1 {
		t.Fatalf("resultAt = %d, want 1", mm.resultAt)
	}
	m = sendKey(mm, "enter")
	mm = m.(Model)
	if mm.inputMode {
		t.Fatal("input should close after jumping to result")
	}
	if mm.current != filepath.Join(root, mm.results[1].path) {
		t.Fatalf("jumped to %q, want %q", mm.current, filepath.Join(root, mm.results[1].path))
	}
}

func TestPathInputSearchEmptyQuery(t *testing.T) {
	m := sendKey(New(tree(t), ""), "/")
	if m.(Model).results != nil {
		t.Fatal("no results expected for empty query")
	}
}

func TestPathInputRankingTrailingSlash(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "kiosks", "public_html", "actions", "admin"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "kiosks", "public_html", "actions", "cron"), 0o755)
	m := sendKey(New(root, ""), "/")
	for _, r := range []string{"a", "c", "t", "i", "o", "n", "s", "/"} {
		m = sendKey(m, r)
	}
	mm := m.(Model)
	// The actions folder itself must be first, despite the trailing slash.
	if len(mm.results) == 0 || mm.results[0].path != "kiosks/public_html/actions" {
		t.Fatalf("results[0] = %v, want the actions folder first (all: %v)", mm.results, mm.results)
	}
}

func TestPathInputRankingPrefixOverSubstring(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "api", "cmd"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "shared", "apicore"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "rapid"), 0o755) // contains "api", low rank
	m := sendKey(New(root, ""), "/")
	m = sendKey(m, "api")
	mm := m.(Model)
	// exact name > name prefix > segment > plain substring
	if len(mm.results) < 3 || mm.results[0].path != "api" || mm.results[1].path != "shared/apicore" || mm.results[2].path != "api/cmd" {
		t.Fatalf("ranking wrong: %v", mm.results)
	}
	if mm.results[len(mm.results)-1].path != "rapid" {
		t.Fatalf("plain substring should rank last: %v", mm.results)
	}
}

func TestPathInputResultsScroll(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 15; i++ {
		_ = os.MkdirAll(filepath.Join(root, fmt.Sprintf("admin%d", i)), 0o755)
	}
	m := sendKey(New(root, ""), "/")
	m = sendKey(m, "admin")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	mm := m.(Model)
	if len(mm.results) != 15 {
		t.Fatalf("results = %d, want 15", len(mm.results))
	}
	// popupLimit at height 20 = min(8, max(3, 10)) = 8
	for i := 0; i < 12; i++ {
		m = sendKey(m, "down")
	}
	mm = m.(Model)
	if mm.resultAt != 12 {
		t.Fatalf("resultAt = %d, want 12", mm.resultAt)
	}
	if mm.resultOffset != 5 {
		t.Fatalf("resultOffset = %d, want 5 (12-8+1)", mm.resultOffset)
	}
	// scrolling back up keeps the window until the cursor leaves it
	for i := 0; i < 6; i++ {
		m = sendKey(m, "up")
	}
	mm = m.(Model)
	if mm.resultAt != 6 || mm.resultOffset != 5 {
		t.Fatalf("resultAt=%d offset=%d, want 6/5 (cursor inside window)", mm.resultAt, mm.resultOffset)
	}
	// all the way up: window snaps back to the top
	for i := 0; i < 6; i++ {
		m = sendKey(m, "up")
	}
	mm = m.(Model)
	if mm.resultAt != 0 || mm.resultOffset != 0 {
		t.Fatalf("resultAt=%d offset=%d, want 0/0", mm.resultAt, mm.resultOffset)
	}
}

func TestMarkMultipleFolders(t *testing.T) {
	root := tree(t)                     // a/, c/, file.txt
	m := sendKey(New(root, ""), "down") // a/
	m = sendKey(m, " ")                 // mark a
	m = sendKey(m, "down")              // c/
	m = sendKey(m, " ")                 // mark c
	m = sendKey(m, "s")                 // finish
	scope, ok := m.(Model).Selected()
	if !ok || len(scope) != 2 || scope[0] != "a" || scope[1] != "c" {
		t.Fatalf("Selected() = (%v, %v), want ([a c], true)", scope, ok)
	}
}

func TestMarkCurrentFolder(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, ""), "down") // a/
	m = sendKey(m, "enter")             // descend into a
	m = sendKey(m, " ")                 // mark current folder (row 0)
	m = sendKey(m, "s")
	scope, ok := m.(Model).Selected()
	if !ok || len(scope) != 1 || scope[0] != "a" {
		t.Fatalf("Selected() = (%v, %v), want ([a], true)", scope, ok)
	}
}

func TestMarkDedupeParentChild(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, ""), "down") // a/
	m = sendKey(m, " ")                 // mark a
	m = sendKey(m, "enter")             // into a: rows [select, .., b/]
	m = sendKey(m, "down")              // ..
	m = sendKey(m, "down")              // b/
	m = sendKey(m, " ")                 // mark a/b
	m = sendKey(m, "s")
	scope, ok := m.(Model).Selected()
	if !ok || len(scope) != 1 || scope[0] != "a" {
		t.Fatalf("Selected() = (%v, %v), want parent-only ([a], true)", scope, ok)
	}
}

func TestMarkRootSelectsFullProject(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, ""), "down") // a/
	m = sendKey(m, " ")                 // mark a
	m = sendKey(m, "up")                // back to row 0
	m = sendKey(m, " ")                 // mark root → full project
	m = sendKey(m, "s")
	scope, ok := m.(Model).Selected()
	if !ok || len(scope) != 0 {
		t.Fatalf("Selected() = (%v, %v), want empty (full project), true", scope, ok)
	}
}

func TestMarkUnmark(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, ""), "down") // a/
	m = sendKey(m, " ")                 // mark a
	m = sendKey(m, " ")                 // unmark a
	m = sendKey(m, "s")
	scope, ok := m.(Model).Selected()
	// No marks left → falls back to the current folder (root = full project).
	if !ok || len(scope) != 0 {
		t.Fatalf("unmark should fall back to current folder, got (%v, %v)", scope, ok)
	}
}

func TestMarkFile(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, ""), "down") // a/
	m = sendKey(m, "down")              // c/
	m = sendKey(m, "down")              // file.txt
	m = sendKey(m, " ")                 // mark the file
	mm := m.(Model)
	if !mm.marked["file.txt"] {
		t.Fatalf("file.txt not marked: %v", mm.marked)
	}
	m = sendKey(mm, "s")
	scope, ok := m.(Model).Selected()
	if !ok || len(scope) != 1 || scope[0] != "file.txt" {
		t.Fatalf("Selected() = (%v, %v), want ([file.txt], true)", scope, ok)
	}
}

func TestMarkFileInsideFolder(t *testing.T) {
	root := tree(t)
	_ = os.WriteFile(filepath.Join(root, "a", "inner.txt"), []byte("x"), 0o644)
	m := sendKey(New(root, ""), "down") // a/
	m = sendKey(m, "enter")             // into a: rows [select, .., b/, inner.txt]
	m = sendKey(m, "down")              // ..
	m = sendKey(m, "down")              // b/
	m = sendKey(m, "down")              // inner.txt
	m = sendKey(m, " ")                 // mark the file
	m = sendKey(m, "s")
	scope, ok := m.(Model).Selected()
	if !ok || len(scope) != 1 || scope[0] != "a/inner.txt" {
		t.Fatalf("Selected() = (%v, %v), want ([a/inner.txt], true)", scope, ok)
	}
}

func TestMarkMixedFileAndFolder(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, ""), "down") // a/
	m = sendKey(m, " ")                 // mark folder a
	m = sendKey(m, "down")              // c/
	m = sendKey(m, "down")              // file.txt
	m = sendKey(m, " ")                 // mark file
	m = sendKey(m, "s")
	scope, ok := m.(Model).Selected()
	if !ok || len(scope) != 2 || scope[0] != "a" || scope[1] != "file.txt" {
		t.Fatalf("Selected() = (%v, %v), want ([a file.txt], true)", scope, ok)
	}
}

func TestMarkFileCoveredByFolderDedupe(t *testing.T) {
	root := tree(t)
	_ = os.WriteFile(filepath.Join(root, "a", "inner.txt"), []byte("x"), 0o644)
	m := sendKey(New(root, ""), "down") // a/
	m = sendKey(m, " ")                 // mark folder a
	m = sendKey(m, "enter")             // into a: rows [select, .., b/, inner.txt]
	m = sendKey(m, "down")              // ..
	m = sendKey(m, "down")              // b/
	m = sendKey(m, "down")              // inner.txt
	m = sendKey(m, " ")                 // mark the file inside a
	m = sendKey(m, "s")
	scope, ok := m.(Model).Selected()
	if !ok || len(scope) != 1 || scope[0] != "a" {
		t.Fatalf("file inside marked folder must dedupe to the folder, got (%v, %v)", scope, ok)
	}
}

func TestPathInputClearsOnReopen(t *testing.T) {
	root := tree(t)
	m := sendKey(New(root, ""), "/")
	m = sendKey(m, "a")     // type a partial path
	m = sendKey(m, "enter") // jump to the matched folder
	mm := m.(Model)
	if mm.current != filepath.Join(root, "a") {
		t.Fatalf("expected to be inside a, got %q", mm.current)
	}
	m = sendKey(mm, "/") // reopen the search
	mm = m.(Model)
	if mm.input.Value() != "" {
		t.Fatalf("input should be cleared on reopen, got %q", mm.input.Value())
	}
	if mm.results != nil {
		t.Fatalf("results should be empty on reopen, got %v", mm.results)
	}
}

func TestIndexAllSkipsHeavyDirs(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "src", "deep"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, ".git", "objects"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "vendor", "lib"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "src", "app.go"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "node_modules", "pkg", "index.js"), []byte("x"), 0o644)
	dirs, files := indexAll(root)
	for _, d := range dirs {
		if d == "node_modules" || strings.HasPrefix(d, "node_modules/") ||
			d == ".git" || strings.HasPrefix(d, ".git/") ||
			d == "vendor" || strings.HasPrefix(d, "vendor/") {
			t.Fatalf("heavy dir leaked into index: %q (all: %v)", d, dirs)
		}
	}
	if len(dirs) != 2 || dirs[0] != "src" || dirs[1] != "src/deep" {
		t.Fatalf("dir index = %v, want [src src/deep]", dirs)
	}
	if len(files) != 1 || files[0] != "src/app.go" {
		t.Fatalf("file index = %v, want [src/app.go]", files)
	}
}

func TestRemoteBrowseKey(t *testing.T) {
	m := sendKey(New(tree(t), ""), "r")
	mm := m.(Model)
	if !mm.RemoteBrowse() {
		t.Fatal("RemoteBrowse() should be true after pressing r")
	}
	if mm.chosen {
		t.Fatal("r must not count as a scope selection")
	}
}

func TestRemoteBrowseKeyDisabledInDirMode(t *testing.T) {
	m := sendKey(PickDir(tree(t)), "r")
	mm := m.(Model)
	if mm.RemoteBrowse() {
		t.Fatal("r must be ignored in directory mode")
	}
}

func TestLocalUploadKey(t *testing.T) {
	m := sendKey(New(tree(t), ""), "u")
	mm := m.(Model)
	if !mm.LocalUpload() {
		t.Fatal("LocalUpload() should be true after pressing u")
	}
	if mm.chosen {
		t.Fatal("u must not count as a scope selection")
	}
}

func TestLocalUploadKeyDisabledInDirMode(t *testing.T) {
	m := sendKey(PickDir(tree(t)), "u")
	mm := m.(Model)
	if mm.LocalUpload() {
		t.Fatal("u must be ignored in directory mode")
	}
}

func TestShellKey(t *testing.T) {
	m := sendKey(New(tree(t), ""), "t")
	mm := m.(Model)
	if !mm.ShellMode() {
		t.Fatal("ShellMode() should be true after pressing t")
	}
	if mm.chosen {
		t.Fatal("t must not count as a scope selection")
	}
}

func TestShellKeyDisabledInDirMode(t *testing.T) {
	m := sendKey(PickDir(tree(t)), "t")
	mm := m.(Model)
	if mm.ShellMode() {
		t.Fatal("t must be ignored in directory mode")
	}
}

func TestViewAdvertisesUploadKey(t *testing.T) {
	root := tree(t)
	m := New(root, "")
	m.width, m.height = 100, 24
	view := m.View()
	if !strings.Contains(view, "[U] upload local files") {
		t.Fatalf("scope mode should advertise the upload key: %q", view)
	}
	if !strings.Contains(view, "[T] shell") {
		t.Fatalf("scope mode should advertise the shell key: %q", view)
	}
	if strings.Contains(PickDir(root).View(), "[U] upload local files") || strings.Contains(PickDir(root).View(), "[T] shell") {
		t.Fatal("dir mode must not advertise the upload or shell keys")
	}
}

func TestPickDirReturnsAbsoluteCurrent(t *testing.T) {
	root := tree(t)
	mm := New(root, "")
	mm.pickDir = true
	m := sendKey(mm, "down") // a/
	m = sendKey(m, "enter")  // into a
	m = sendKey(m, "s")      // finish
	sel, ok := m.(Model).Selected()
	if !ok || len(sel) != 1 || sel[0] != filepath.Join(root, "a") {
		t.Fatalf("Selected() = (%v, %v), want ([%s], true)", sel, ok, filepath.Join(root, "a"))
	}
}

func TestPickDirAtRootReturnsRoot(t *testing.T) {
	root := tree(t)
	m := sendKey(PickDir(root), "s")
	sel, ok := m.(Model).Selected()
	if !ok || len(sel) != 1 || sel[0] != root {
		t.Fatalf("Selected() = (%v, %v), want ([%s], true)", sel, ok, root)
	}
}

func TestPickDirSpaceDisabled(t *testing.T) {
	m := sendKey(PickDir(tree(t)), " ")
	mm := m.(Model)
	if len(mm.marked) != 0 {
		t.Fatalf("marked = %v, want none in directory mode", mm.marked)
	}
	if !strings.Contains(mm.status, "single destination") {
		t.Fatalf("status = %q, want single-destination hint", mm.status)
	}
}

func TestPickDirViewHeaderAndHints(t *testing.T) {
	root := tree(t)
	m := PickDir(root)
	m.width, m.height = 80, 24
	view := m.View()
	if !strings.Contains(view, "Select download destination") {
		t.Fatalf("view missing pick-dir header: %q", view)
	}
	if strings.Contains(view, "[R] browse remote") {
		t.Fatalf("dir mode must not advertise remote browse: %q", view)
	}
	if !strings.Contains(view, "Destination:") {
		t.Fatalf("view missing destination status: %q", view)
	}
}

func TestListScrollsWithCursor(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 12; i++ {
		_ = os.MkdirAll(filepath.Join(root, fmt.Sprintf("d%02d", i)), 0o755)
	}
	mm := New(root, "")
	mm.width, mm.height = 80, 8 // vis = max(3, 3) = 3 rows
	if mm.rowCount() != 13 {    // select + 12 dirs
		t.Fatalf("rowCount = %d, want 13", mm.rowCount())
	}
	m := sendKey(mm, "end") // last row
	mm2 := m.(Model)
	if mm2.offset != 10 {
		t.Fatalf("offset = %d, want 10 (cursor %d)", mm2.offset, mm2.cursor)
	}
	view := mm2.View()
	if !strings.Contains(view, "d11/") {
		t.Fatalf("scrolled view must show the cursor row: %q", view)
	}
	m = sendKey(mm2, "home")
	if m.(Model).offset != 0 {
		t.Fatalf("offset = %d, want 0 after home", m.(Model).offset)
	}
}

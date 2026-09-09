package tui

import (
	"strings"
	"testing"

	"preflight/internal/domain"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const sampleUnifiedDiff = `--- /remote/main.go
+++ /local/main.go
@@ -1,4 +1,5 @@
 package main
-func message() string { return "old" }
+func message() string { return "new" }
+func enabled() bool { return true }
 
 func main() {}
`

func TestParseUnifiedDiffAlignsRemoteAndLocalChanges(t *testing.T) {
	rows, sections := parseUnifiedDiff(sampleUnifiedDiff)
	if len(rows) != 6 {
		t.Fatalf("rows = %d, want 6", len(rows))
	}
	if rows[0].hunk == "" {
		t.Fatal("first row should describe the hunk")
	}
	if rows[2].oldNumber != 2 || rows[2].newNumber != 2 {
		t.Fatalf("replacement line numbers = %d/%d, want 2/2", rows[2].oldNumber, rows[2].newNumber)
	}
	if rows[2].oldKind != diffCellRemoved || rows[2].newKind != diffCellAdded {
		t.Fatalf("replacement kinds = %v/%v, want removed/added", rows[2].oldKind, rows[2].newKind)
	}
	if rows[3].oldKind != diffCellEmpty || rows[3].newKind != diffCellAdded || rows[3].newNumber != 3 {
		t.Fatalf("insert row was not aligned correctly: %#v", rows[3])
	}
	if rows[5].oldNumber != 4 || rows[5].newNumber != 5 {
		t.Fatalf("trailing context line numbers = %d/%d, want 4/5", rows[5].oldNumber, rows[5].newNumber)
	}

	if len(sections) != 1 {
		t.Fatalf("sections = %d, want 1", len(sections))
	}
	sec := sections[0]
	if sec.LocalStartLine != 2 || sec.LocalLineCount != 2 {
		t.Fatalf("sec local lines = %d:%d, want 2:2", sec.LocalStartLine, sec.LocalLineCount)
	}
	if sec.RemoteStartLine != 2 || sec.RemoteLineCount != 1 {
		t.Fatalf("sec remote lines = %d:%d, want 2:1", sec.RemoteStartLine, sec.RemoteLineCount)
	}
	if len(sec.RemoteLines) != 1 || sec.RemoteLines[0] != `func message() string { return "old" }` {
		t.Fatalf("unexpected remote lines: %#v", sec.RemoteLines)
	}
}

func TestRenderSplitDiffEmbedsPullButton(t *testing.T) {
	rendered := renderSplitDiff("main.go", "edits detected", sampleUnifiedDiff, 100, true)
	if !strings.Contains(rendered, "[→]") {
		t.Fatalf("rendered diff missing [→] button:\n%s", rendered)
	}
}

func TestWrapLine(t *testing.T) {
	longLine := "func TestSomething(paramA string, paramB int, paramC bool) (string, error) { return \"ok\", nil }"
	chunks := wrapLine(longLine, 30)
	if len(chunks) < 3 {
		t.Fatalf("expected at least 3 chunks, got %d: %#v", len(chunks), chunks)
	}
	for i, chunk := range chunks {
		if lipgloss.Width(chunk) > 30 {
			t.Fatalf("chunk %d width = %d > 30: %q", i, lipgloss.Width(chunk), chunk)
		}
	}

	line14 := "        'UPDATE transactions SET transactions_notes = CONCAT(transactions_notes, ?) WHERE transactions_id = ?',"
	chunks14 := wrapLine(line14, 109)
	if len(chunks14) != 2 {
		t.Fatalf("expected 2 chunks for line 14 at width 109, got %d: %#v", len(chunks14), chunks14)
	}
}

func TestReproduceLine14(t *testing.T) {
	diffContent := "@@ -1,11 +1,16 @@\n" +
		" }\n" +
		"+function overdue_final($db, $transactions_id, $reason) {\n" +
		"+    $db->query(\n" +
		"+        'UPDATE transactions SET transactions_notes = CONCAT(transactions_notes, ?) WHERE transactions_id = ?',\n" +
		"+        array(' | overdue_final:' . $reason, $transactions_id)\n" +
		"+    );\n"
	rendered := renderSplitDiff("test.php", "test", diffContent, 235, true)
	lines := strings.Split(rendered, "\n")
	if len(lines) != 12 {
		t.Fatalf("expected exactly 12 lines, got %d", len(lines))
	}
	// Line 8 is the first wrapped line, Line 9 is the second wrapped line (continuation)
	if !strings.Contains(lines[8], "WHERE transactions_id =") {
		t.Errorf("line 8 should contain first part of UPDATE query: %q", lines[8])
	}
	if !strings.Contains(lines[9], "?',") {
		t.Errorf("line 9 should contain continuation: %q", lines[9])
	}
	if !strings.Contains(lines[10], "array(' | overdue_final:'") {
		t.Errorf("line 10 should be the next line (array): %q", lines[10])
	}
}

func TestRenderSplitDiffWordWrapAligned(t *testing.T) {
	diffWithLongLine := `--- /remote/main.go
+++ /local/main.go
@@ -1,2 +1,2 @@
-func Short() {}
+func VeryLongFunctionWithManyArguments(firstArgument string, secondArgument int, thirdArgument bool) (result string, err error) {}
`
	rendered := renderSplitDiff("main.go", "1 edit", diffWithLongLine, 80, true)
	if strings.Contains(rendered, "…") {
		t.Fatalf("rendered diff with wrap=true should not contain ellipsis truncation:\n%s", rendered)
	}
	if !strings.Contains(rendered, "VeryLongFunction") {
		t.Fatalf("rendered diff should contain function name:\n%s", rendered)
	}
	if !strings.Contains(rendered, "uments") {
		t.Fatalf("rendered diff should contain wrapped function name continuation:\n%s", rendered)
	}
	if !strings.Contains(rendered, "thirdArgument") {
		t.Fatalf("rendered diff should contain wrapped arguments:\n%s", rendered)
	}
}

func TestDiffPreviewHasSplitLabelsAndDetectedLanguage(t *testing.T) {
	m := newModel([]*domain.SyncFileItem{{
		RelativePath: "main.go",
		Action:       domain.ActionUpload,
		DiffDetails: &domain.DiffResult{
			Summary: "2 edits detected",
			Unified: sampleUnifiedDiff,
		},
	}})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = updated.(Model)
	preview := m.viewport.View()
	for _, expected := range []string{"REMOTE", "LOCAL", "syntax: Go"} {
		if !strings.Contains(preview, expected) {
			t.Fatalf("preview missing %q:\n%s", expected, preview)
		}
	}
}

func TestFocusedDiffUsesFullTerminalWidth(t *testing.T) {
	m := newModel([]*domain.SyncFileItem{{
		RelativePath: "main.go",
		Action:       domain.ActionUpload,
		DiffDetails:  &domain.DiffResult{Unified: sampleUnifiedDiff},
	}})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = updated.(Model)
	previewWidth := m.viewport.Width

	updated, _ = m.Update(key("tab"))
	m = updated.(Model)
	if !m.focusDiff {
		t.Fatal("Tab should focus the diff")
	}
	if m.viewport.Width <= previewWidth || m.viewport.Width != 116 {
		t.Fatalf("focused width = %d, preview width = %d; want focused width 116", m.viewport.Width, previewWidth)
	}
	if !strings.Contains(m.View(), "Back to files") {
		t.Fatalf("focused toolbar missing navigation hint:\n%s", m.View())
	}
}

func TestDiffPreviewCapsOversizedUnifiedDiff(t *testing.T) {
	unified := strings.Repeat("+changed\n", domain.MaxDiffLines+1)
	m := newModel([]*domain.SyncFileItem{{
		RelativePath: "large.csv",
		Action:       domain.ActionUpload,
		DiffDetails:  &domain.DiffResult{Unified: unified},
	}})

	preview := m.viewport.View()
	if !strings.Contains(preview, "(diff too large)") {
		t.Fatalf("preview did not cap oversized diff: %q", preview)
	}
	if strings.Contains(preview, "REMOTE") || strings.Contains(preview, "+changed") {
		t.Fatalf("preview rendered oversized diff content: %q", preview)
	}
}

func TestOversizedDiffCanBeViewedAnyway(t *testing.T) {
	unified := "--- /remote/large.csv\n+++ /local/large.csv\n@@ -1,1 +1,1 @@\n" +
		strings.Repeat("-remote\n+local\n", domain.MaxDiffLines/2+1)
	m := newModel([]*domain.SyncFileItem{{
		RelativePath: "large.csv",
		Action:       domain.ActionUpload,
		DiffDetails:  &domain.DiffResult{Unified: unified, TooLarge: true},
	}})

	updated, _ := m.Update(key("v"))
	m = updated.(Model)
	preview := m.viewport.View()
	if strings.Contains(preview, "(diff too large)") {
		t.Fatalf("forced preview still shows the size guard: %q", preview)
	}
	if !strings.Contains(preview, "REMOTE") || !strings.Contains(preview, "remote") {
		t.Fatalf("forced preview did not render the diff: %q", preview)
	}
}

func TestOversizedDiffLoadsDiscardedContentOnDemand(t *testing.T) {
	item := &domain.SyncFileItem{
		RelativePath: "large.csv",
		Action:       domain.ActionUpload,
		DiffDetails:  &domain.DiffResult{TooLarge: true},
	}
	m := newModel([]*domain.SyncFileItem{item})
	m.loadDiff = func(got *domain.SyncFileItem) (*domain.DiffResult, error) {
		if got != item {
			t.Fatalf("loader item = %p, want %p", got, item)
		}
		return &domain.DiffResult{Unified: sampleUnifiedDiff}, nil
	}

	updated, cmd := m.Update(key("v"))
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("view anyway did not start the on-demand load")
	}
	if !strings.Contains(m.status, "loading full diff") {
		t.Fatalf("loading status = %q", m.status)
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if !strings.Contains(m.viewport.View(), "REMOTE") {
		t.Fatalf("loaded diff was not rendered: %q", m.viewport.View())
	}
}

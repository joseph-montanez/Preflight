package tui

import (
	"strings"
	"testing"

	"godeploy/internal/domain"

	tea "github.com/charmbracelet/bubbletea"
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
	rows := parseUnifiedDiff(sampleUnifiedDiff)
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

package manage

import (
	"testing"

	"preflight/internal/config"

	tea "github.com/charmbracelet/bubbletea"
)

func TestValidSteps(t *testing.T) {
	tests := []struct {
		name    string
		rows    [][4]string
		want    []config.TaskStep
		wantErr string
	}{
		{name: "empty editor", rows: nil, want: nil, wantErr: ""},
		{name: "blank rows skipped", rows: [][4]string{{"", "", "", ""}}, want: nil, wantErr: ""},
		{name: "full rows", rows: [][4]string{{"a", "dir", "make", ""}, {"b", "", "up", ""}},
			want:    []config.TaskStep{{AccountID: "a", Workdir: "dir", Run: "make"}, {AccountID: "b", Run: "up"}},
			wantErr: ""},
		{name: "login shell kept", rows: [][4]string{{"a", "dir", "make", "bash -lc"}},
			want:    []config.TaskStep{{AccountID: "a", Workdir: "dir", Run: "make", Shell: "bash -lc"}},
			wantErr: ""},
		{name: "missing account", rows: [][4]string{{"", "dir", "make", ""}}, wantErr: "row 1: account is required"},
		{name: "missing command", rows: [][4]string{{"a", "", "  ", ""}}, wantErr: "row 1: command is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &stepEditor{}
			for _, row := range tt.rows {
				e.rows = append(e.rows, newStepInputRow(row[0], row[1], row[2], row[3]))
			}
			got, err := e.validSteps()
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("validSteps() = %v, want %q", got, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("validSteps() = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("validSteps()[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestStepEditorApplyCancel(t *testing.T) {
	e := newStepEditor([]config.TaskStep{{AccountID: "a", Run: "x"}}, []string{"a"})
	if _, err := e.Update(tea.KeyMsg{Type: tea.KeyEnter}); err == nil || err.Error() != "task steps saved" {
		t.Fatalf("enter should apply: %v", err)
	}
	if len(e.result) != 1 || e.result[0].Run != "x" {
		t.Fatalf("result = %+v", e.result)
	}

	e = newStepEditor(nil, nil)
	if _, err := e.Update(tea.KeyMsg{Type: tea.KeyEsc}); err == nil || err.Error() != "task steps cancelled" {
		t.Fatalf("esc should cancel: %v", err)
	}
}

func TestStepEditorAccountCompletion(t *testing.T) {
	e := newStepEditor(nil, []string{"kiosks", "kiosk-root"})
	e = sendStepKey(e, "kio")
	e = sendStepKey(e, "tab") // several matches -> popup
	if e.popup == nil || len(e.popup) != 2 {
		t.Fatalf("expected account popup, got %v", e.popup)
	}
	e = sendStepKey(e, "enter") // pick first
	if got := e.rows[0].inputs[0].Value(); got != "kiosks" {
		t.Fatalf("account = %q, want kiosks", got)
	}

	e = newStepEditor(nil, []string{"kiosks"})
	e = sendStepKey(e, "kiosks")
	e = sendStepKey(e, "tab") // exact match advances to workdir
	if e.column != 1 {
		t.Fatalf("column = %d, want workdir after exact match", e.column)
	}

	e = newStepEditor(nil, []string{"kiosks"})
	e = sendStepKey(e, "ghost")
	e = sendStepKey(e, "tab") // no match stays put
	if e.column != 0 || e.status == "" {
		t.Fatalf("no-match should stay with a status: col=%d status=%q", e.column, e.status)
	}
}

func TestStepEditorRows(t *testing.T) {
	e := newStepEditor(nil, []string{"a"})
	if len(e.rows) != 1 {
		t.Fatalf("empty editor should start with one row, got %d", len(e.rows))
	}
	e = sendStepKey(e, "ctrl+n")
	if len(e.rows) != 2 || e.cursor != 1 {
		t.Fatalf("add should append a focused row: rows=%d cursor=%d", len(e.rows), e.cursor)
	}
	e = sendStepKey(e, "ctrl+up")
	if e.cursor != 0 {
		t.Fatalf("reorder should move the row up: cursor=%d", e.cursor)
	}
	e = sendStepKey(e, "ctrl+d")
	if len(e.rows) != 1 {
		t.Fatalf("delete should remove the row: rows=%d", len(e.rows))
	}
}

func sendStepKey(e *stepEditor, s string) *stepEditor {
	var msg tea.KeyMsg
	switch s {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		msg = tea.KeyMsg{Type: tea.KeyTab}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	updated, _ := e.Update(msg)
	_ = updated
	return e
}

package manage

import (
	"errors"
	"fmt"
	"strings"

	"preflight/internal/config"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	errStepsSaved  = errors.New("task steps saved")
	errStepsCancel = errors.New("task steps cancelled")
)

type stepInputRow struct {
	inputs [5]textinput.Model // account, workdir/local, command/remote, shell, kind
}

// stepEditor is a focused, row-based editor for task steps. Columns stay
// account-first so run entry matches the old flow; the trailing kind
// column selects run vs upload. Run rows use workdir/command/shell;
// upload rows reuse those columns as local/remote with a blank shell.
// Enter applies, Esc cancels.
type stepEditor struct {
	accounts []string
	rows     []stepInputRow
	cursor   int
	column   int
	offset   int

	popup   []string
	popupAt int
	status  string
	result  []config.TaskStep
}

func newStepEditor(steps []config.TaskStep, accounts []string) *stepEditor {
	editor := &stepEditor{accounts: accounts}
	for _, step := range steps {
		if step.IsUpload() {
			editor.rows = append(editor.rows, newStepInputRow(
				step.AccountID, step.Upload, step.Remote, "", config.TaskStepUpload))
		} else {
			editor.rows = append(editor.rows, newStepInputRow(
				step.AccountID, step.Workdir, step.Run, step.Shell, config.TaskStepRun))
		}
	}
	if len(editor.rows) == 0 {
		editor.rows = append(editor.rows, newStepInputRow("", "", "", "", ""))
	}
	editor.focus()
	return editor
}

func newStepInputRow(account, middle1, middle2, shell, kind string) stepInputRow {
	accountInput := textinput.New()
	accountInput.Prompt = ""
	accountInput.CharLimit = 256
	accountInput.Placeholder = "account"
	accountInput.SetValue(account)

	middle1Input := textinput.New()
	middle1Input.Prompt = ""
	middle1Input.CharLimit = 1024
	middle1Input.Placeholder = "workdir / local"
	middle1Input.SetValue(middle1)

	middle2Input := textinput.New()
	middle2Input.Prompt = ""
	middle2Input.CharLimit = 4096
	middle2Input.Placeholder = "command / remote"
	middle2Input.SetValue(middle2)

	shellInput := textinput.New()
	shellInput.Prompt = ""
	shellInput.CharLimit = 256
	shellInput.Placeholder = "shell, e.g. bash -lc (run only)"
	shellInput.SetValue(shell)

	kindInput := textinput.New()
	kindInput.Prompt = ""
	kindInput.CharLimit = 16
	kindInput.Placeholder = "run"
	kindInput.SetValue(kind)
	return stepInputRow{inputs: [5]textinput.Model{accountInput, middle1Input, middle2Input, shellInput, kindInput}}
}

func (e *stepEditor) focus() {
	for row := range e.rows {
		for column := range e.rows[row].inputs {
			if row == e.cursor && column == e.column {
				e.rows[row].inputs[column].Focus()
			} else {
				e.rows[row].inputs[column].Blur()
			}
		}
	}
}

func (e *stepEditor) moveRow(delta int) {
	if len(e.rows) == 0 {
		return
	}
	e.cursor = min(max(0, e.cursor+delta), len(e.rows)-1)
	e.popup = nil
	e.focus()
}

func (e *stepEditor) setColumn(column int) {
	e.column = min(max(0, column), 4)
	e.popup = nil
	e.focus()
}

func (e *stepEditor) addRow() {
	row := newStepInputRow("", "", "", "", "")
	insertAt := min(e.cursor+1, len(e.rows))
	e.rows = append(e.rows, stepInputRow{})
	copy(e.rows[insertAt+1:], e.rows[insertAt:])
	e.rows[insertAt] = row
	e.cursor, e.column = insertAt, 0
	e.popup = nil
	e.status = "step added"
	e.focus()
}

func (e *stepEditor) deleteRow() {
	if len(e.rows) <= 1 {
		e.rows = nil
		e.cursor, e.column = 0, 0
	} else {
		e.rows = append(e.rows[:e.cursor], e.rows[e.cursor+1:]...)
		e.cursor = min(e.cursor, len(e.rows)-1)
	}
	e.popup = nil
	e.status = "step removed"
	e.focus()
}

func (e *stepEditor) reorder(delta int) {
	if len(e.rows) == 0 {
		return
	}
	target := e.cursor + delta
	if target < 0 || target >= len(e.rows) {
		return
	}
	e.rows[e.cursor], e.rows[target] = e.rows[target], e.rows[e.cursor]
	e.cursor = target
	e.popup = nil
	e.focus()
}

func (e *stepEditor) currentValue() string {
	if len(e.rows) == 0 {
		return ""
	}
	return e.rows[e.cursor].inputs[e.column].Value()
}

func (e *stepEditor) setCurrentValue(value string) {
	if len(e.rows) == 0 {
		column := e.column
		e.addRow()
		e.setColumn(column)
	}
	e.rows[e.cursor].inputs[e.column].SetValue(value)
	e.rows[e.cursor].inputs[e.column].CursorEnd()
}

// completeAccount offers the configured account ids matching the current
// account cell. One match fills it (a second Tab on the exact match moves
// to the workdir column); several open a picker popup; empty shows all.
// On the trailing kind column, Tab toggles run/upload instead.
func (e *stepEditor) completeAccount() {
	if e.column == 4 {
		e.completeKind()
		return
	}
	if e.column != 0 {
		e.setColumn((e.column + 1) % 5)
		return
	}
	prefix := e.currentValue()
	var matches []string
	for _, id := range e.accounts {
		if strings.HasPrefix(id, prefix) {
			matches = append(matches, id)
		}
	}
	switch {
	case len(matches) == 1 && matches[0] == prefix:
		e.setColumn(1)
	case len(matches) == 1:
		e.setCurrentValue(matches[0])
		e.status = ""
	case len(matches) == 0:
		e.status = "no matching account"
	default:
		e.popup, e.popupAt = matches, 0
	}
}

// completeKind toggles the kind cell between run and upload and stays
// put, so repeated Tab flips it back. An empty cell (the run default)
// becomes upload first so Tab discovers the alternative.
func (e *stepEditor) completeKind() {
	switch strings.ToLower(strings.TrimSpace(e.currentValue())) {
	case config.TaskStepUpload:
		e.setCurrentValue(config.TaskStepRun)
	default:
		e.setCurrentValue(config.TaskStepUpload)
	}
	e.status = ""
	e.focus()
}

func (e *stepEditor) Update(msg tea.Msg) (tea.Cmd, error) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return e.handleKey(msg)
	}
	return nil, nil
}

func (e *stepEditor) handleKey(msg tea.KeyMsg) (tea.Cmd, error) {
	key := msg.String()
	if e.popup != nil {
		switch key {
		case "tab", "down", "j", "right":
			e.popupAt = (e.popupAt + 1) % len(e.popup)
		case "shift+tab", "up", "k", "left":
			e.popupAt = (e.popupAt - 1 + len(e.popup)) % len(e.popup)
		case "enter":
			e.setCurrentValue(e.popup[e.popupAt])
			e.popup = nil
		case "esc":
			e.popup = nil
			e.status = ""
		default:
			e.popup = nil
			return e.typeInto(msg)
		}
		return nil, nil
	}

	switch key {
	case "ctrl+c", "esc":
		return nil, errStepsCancel
	case "enter", "ctrl+s":
		steps, err := e.validSteps()
		if err != nil {
			e.status = err.Error()
			return nil, nil
		}
		e.result = steps
		return nil, errStepsSaved
	case "tab":
		e.completeAccount()
		return nil, nil
	case "shift+tab":
		e.setColumn((e.column + 4) % 5)
		return nil, nil
	case "up":
		e.moveRow(-1)
		return nil, nil
	case "down":
		e.moveRow(1)
		return nil, nil
	case "ctrl+left":
		e.setColumn(0)
		return nil, nil
	case "ctrl+right":
		e.setColumn(4)
		return nil, nil
	case "ctrl+n":
		e.addRow()
		return nil, nil
	case "ctrl+d":
		e.deleteRow()
		return nil, nil
	case "ctrl+up":
		e.reorder(-1)
		return nil, nil
	case "ctrl+down":
		e.reorder(1)
		return nil, nil
	}
	return e.typeInto(msg)
}

func (e *stepEditor) typeInto(msg tea.KeyMsg) (tea.Cmd, error) {
	if len(e.rows) == 0 {
		column := e.column
		e.addRow()
		e.setColumn(column)
	}
	input := &e.rows[e.cursor].inputs[e.column]
	updated, cmd := input.Update(msg)
	*input = updated
	e.status = ""
	return cmd, nil
}

// validSteps drops fully blank rows and maps the rest to run or upload
// steps by the kind cell (empty means run). The editor shape enforces
// the ISP split structurally: run rows carry workdir/command/shell,
// upload rows carry local/remote with a blank shell. Account existence
// and protocol are checked when the task form saves.
func (e *stepEditor) validSteps() ([]config.TaskStep, error) {
	var steps []config.TaskStep
	for i := range e.rows {
		account := strings.TrimSpace(e.rows[i].inputs[0].Value())
		middle1 := strings.TrimSpace(e.rows[i].inputs[1].Value())
		middle2 := strings.TrimSpace(e.rows[i].inputs[2].Value())
		shell := strings.TrimSpace(e.rows[i].inputs[3].Value())
		kind := strings.ToLower(strings.TrimSpace(e.rows[i].inputs[4].Value()))
		if account == "" && middle1 == "" && middle2 == "" && shell == "" && kind == "" {
			continue
		}
		if account == "" {
			return nil, fmt.Errorf("row %d: account is required", i+1)
		}
		switch kind {
		case "", config.TaskStepRun:
			if middle2 == "" {
				return nil, fmt.Errorf("row %d: command is required", i+1)
			}
			steps = append(steps, config.TaskStep{AccountID: account, Workdir: middle1, Run: middle2, Shell: shell})
		case config.TaskStepUpload:
			if middle1 == "" {
				return nil, fmt.Errorf("row %d: local path is required", i+1)
			}
			if middle2 == "" {
				return nil, fmt.Errorf("row %d: remote path is required", i+1)
			}
			if shell != "" {
				return nil, fmt.Errorf("row %d: shell applies to run steps only", i+1)
			}
			steps = append(steps, config.TaskStep{AccountID: account, Upload: middle1, Remote: middle2})
		default:
			return nil, fmt.Errorf("row %d: kind must be run or upload", i+1)
		}
	}
	return steps, nil
}

func (e *stepEditor) View(width, height int) string {
	boxWidth := min(110, max(60, width-4))
	boxHeight := max(8, height-4)
	contentWidth := boxWidth - 4
	separator := " │ "
	prefixWidth := 5
	columnsWidth := contentWidth - prefixWidth - 4*lipgloss.Width(separator)
	accountWidth := columnsWidth * 2 / 10
	middle1Width := columnsWidth * 2 / 10
	shellWidth := columnsWidth * 2 / 10
	kindWidth := columnsWidth / 10
	middle2Width := columnsWidth - accountWidth - middle1Width - shellWidth - kindWidth

	popupRows := min(12, max(4, height-10))
	maxRows := max(1, height-11)
	if e.popup != nil {
		maxRows = max(1, height-popupRows-11)
	}
	if e.cursor < e.offset {
		e.offset = e.cursor
	}
	if e.cursor >= e.offset+maxRows {
		e.offset = e.cursor - maxRows + 1
	}
	end := min(len(e.rows), e.offset+maxRows)

	rows := []string{
		titleStyle.Render(" Task steps"),
		faintStyle.Render(" One command or upload per row, in run order. Tab completes the account; Tab on KIND toggles run/upload."),
		fmt.Sprintf("%-*s%s%-*s%s%-*s%s%-*s%s%-*s", prefixWidth+accountWidth, "    ACCOUNT", separator,
			middle1Width, "WORKDIR/LOCAL", separator, middle2Width, "COMMAND/REMOTE", separator,
			shellWidth, "SHELL", separator, kindWidth, "KIND"),
	}
	if len(e.rows) == 0 {
		rows = append(rows, faintStyle.Render("     No steps — Ctrl+N adds one."))
	}
	for i := e.offset; i < end; i++ {
		e.rows[i].inputs[0].Width = max(6, accountWidth)
		e.rows[i].inputs[1].Width = max(6, middle1Width)
		e.rows[i].inputs[2].Width = max(8, middle2Width)
		e.rows[i].inputs[3].Width = max(6, shellWidth)
		e.rows[i].inputs[4].Width = max(4, kindWidth)
		marker := "  "
		if i == e.cursor {
			marker = "▸ "
		}
		prefix := fmt.Sprintf("%s%2d ", marker, i+1)
		line := prefix +
			truncate(e.rows[i].inputs[0].View(), accountWidth) + faintStyle.Render(separator) +
			truncate(e.rows[i].inputs[1].View(), middle1Width) + faintStyle.Render(separator) +
			truncate(e.rows[i].inputs[2].View(), middle2Width) + faintStyle.Render(separator) +
			truncate(e.rows[i].inputs[3].View(), shellWidth) + faintStyle.Render(separator) +
			truncate(e.rows[i].inputs[4].View(), kindWidth)
		rows = append(rows, truncate(line, contentWidth))
		if i == e.cursor && e.popup != nil {
			rows = append(rows, strings.Repeat(" ", prefixWidth)+e.renderPopup(contentWidth-prefixWidth-4, popupRows))
		}
	}
	if e.offset > 0 || end < len(e.rows) {
		rows = append(rows, faintStyle.Render(fmt.Sprintf(" showing %d-%d of %d steps", e.offset+1, end, len(e.rows))))
	}
	help := "↑/↓ row · Tab account/column · Ctrl+N add · Ctrl+D remove · Ctrl+↑/↓ reorder"
	footer := []string{faintStyle.Render(truncate(help, contentWidth)), faintStyle.Render("Enter apply · Esc cancel")}
	if e.status != "" {
		style := faintStyle
		if strings.Contains(e.status, "required") {
			style = errorStyle
		}
		footer = append(footer, style.Render(truncate(e.status, contentWidth)))
	}
	usedHeight := lipgloss.Height(strings.Join(rows, "\n")) + lipgloss.Height(strings.Join(footer, "\n"))
	for usedHeight < boxHeight {
		rows = append(rows, "")
		usedHeight++
	}
	rows = append(rows, footer...)
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Width(boxWidth).Height(boxHeight).Padding(0, 1)
	return box.Render(strings.Join(rows, "\n"))
}

func (e *stepEditor) renderPopup(width, maxShow int) string {
	rows := make([]string, 0, min(len(e.popup), maxShow)+1)
	for i, cand := range e.popup {
		if i >= maxShow {
			rows = append(rows, faintStyle.Render(fmt.Sprintf("  … %d more", len(e.popup)-maxShow)))
			break
		}
		marker := "  "
		if i == e.popupAt {
			marker = "▸ "
		}
		rows = append(rows, marker+truncate(cand, width-6))
	}
	return lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(0, 1).Render(strings.Join(rows, "\n"))
}

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
	inputs [4]textinput.Model // account, workdir, run, shell
}

// stepEditor is a focused, row-based editor for task steps. The account
// column completes from the configured SFTP account ids; workdir and run
// are free text. Enter applies, Esc cancels.
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
		editor.rows = append(editor.rows, newStepInputRow(step.AccountID, step.Workdir, step.Run, step.Shell))
	}
	if len(editor.rows) == 0 {
		editor.rows = append(editor.rows, newStepInputRow("", "", "", ""))
	}
	editor.focus()
	return editor
}

func newStepInputRow(account, workdir, run, shell string) stepInputRow {
	accountInput := textinput.New()
	accountInput.Prompt = ""
	accountInput.CharLimit = 256
	accountInput.Placeholder = "account"
	accountInput.SetValue(account)

	workdirInput := textinput.New()
	workdirInput.Prompt = ""
	workdirInput.CharLimit = 1024
	workdirInput.Placeholder = "workdir (optional)"
	workdirInput.SetValue(workdir)

	runInput := textinput.New()
	runInput.Prompt = ""
	runInput.CharLimit = 4096
	runInput.Placeholder = "make build"
	runInput.SetValue(run)

	shellInput := textinput.New()
	shellInput.Prompt = ""
	shellInput.CharLimit = 256
	shellInput.Placeholder = "shell, e.g. bash -lc (optional)"
	shellInput.SetValue(shell)
	return stepInputRow{inputs: [4]textinput.Model{accountInput, workdirInput, runInput, shellInput}}
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
	e.column = min(max(0, column), 3)
	e.popup = nil
	e.focus()
}

func (e *stepEditor) addRow() {
	row := newStepInputRow("", "", "", "")
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
func (e *stepEditor) completeAccount() {
	if e.column != 0 {
		e.setColumn((e.column + 1) % 4)
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
		e.setColumn((e.column + 3) % 4)
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
		e.setColumn(3)
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

// validSteps drops fully blank rows and requires an account and a command
// on the rest. Account existence and protocol are checked when the task
// form saves.
func (e *stepEditor) validSteps() ([]config.TaskStep, error) {
	var steps []config.TaskStep
	for i := range e.rows {
		account := strings.TrimSpace(e.rows[i].inputs[0].Value())
		workdir := strings.TrimSpace(e.rows[i].inputs[1].Value())
		run := strings.TrimSpace(e.rows[i].inputs[2].Value())
		shell := strings.TrimSpace(e.rows[i].inputs[3].Value())
		if account == "" && workdir == "" && run == "" && shell == "" {
			continue
		}
		if account == "" {
			return nil, fmt.Errorf("row %d: account is required", i+1)
		}
		if run == "" {
			return nil, fmt.Errorf("row %d: command is required", i+1)
		}
		steps = append(steps, config.TaskStep{AccountID: account, Workdir: workdir, Run: run, Shell: shell})
	}
	return steps, nil
}

func (e *stepEditor) View(width, height int) string {
	boxWidth := min(110, max(60, width-4))
	boxHeight := max(8, height-4)
	contentWidth := boxWidth - 4
	separator := " │ "
	prefixWidth := 5
	columnsWidth := contentWidth - prefixWidth - 3*lipgloss.Width(separator)
	accountWidth := columnsWidth / 5
	workdirWidth := columnsWidth / 5
	shellWidth := columnsWidth / 5
	runWidth := columnsWidth - accountWidth - workdirWidth - shellWidth

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
		faintStyle.Render(" One command per row, in run order. Tab completes the account."),
		fmt.Sprintf("%-*s%s%-*s%s%-*s%s%-*s", prefixWidth+accountWidth, "    ACCOUNT", separator,
			workdirWidth, "WORKDIR", separator, runWidth, "COMMAND", separator, shellWidth, "SHELL"),
	}
	if len(e.rows) == 0 {
		rows = append(rows, faintStyle.Render("     No steps — Ctrl+N adds one."))
	}
	for i := e.offset; i < end; i++ {
		e.rows[i].inputs[0].Width = max(6, accountWidth)
		e.rows[i].inputs[1].Width = max(6, workdirWidth)
		e.rows[i].inputs[2].Width = max(8, runWidth)
		e.rows[i].inputs[3].Width = max(6, shellWidth)
		marker := "  "
		if i == e.cursor {
			marker = "▸ "
		}
		prefix := fmt.Sprintf("%s%2d ", marker, i+1)
		line := prefix +
			truncate(e.rows[i].inputs[0].View(), accountWidth) + faintStyle.Render(separator) +
			truncate(e.rows[i].inputs[1].View(), workdirWidth) + faintStyle.Render(separator) +
			truncate(e.rows[i].inputs[2].View(), runWidth) + faintStyle.Render(separator) +
			truncate(e.rows[i].inputs[3].View(), shellWidth)
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

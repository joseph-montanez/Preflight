package manage

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"godeploy/internal/config"
	"godeploy/internal/pathcomp"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	errMappingsSaved  = errors.New("path mappings saved")
	errMappingsCancel = errors.New("path mappings cancelled")
)

type mappingCompletionMsg struct {
	row        int
	column     int
	candidates []string
	err        error
}

type mappingInputRow struct {
	inputs [2]textinput.Model // local, remote
}

// mappingEditor is a focused, row-based editor for project path mappings.
// Local paths stay relative to localRoot while completion operates on the
// corresponding real directory. Remote completion reuses the account's live
// connection.
type mappingEditor struct {
	localRoot      string
	completeRemote func(string) ([]string, error)
	rows           []mappingInputRow
	cursor         int
	column         int
	offset         int

	popup    []string
	popupAt  int
	popupErr string
	busy     string
	status   string
	result   []config.PathMapping
}

func newMappingEditor(localRoot string, mappings []config.PathMapping, completeRemote func(string) ([]string, error)) *mappingEditor {
	editor := &mappingEditor{localRoot: strings.TrimSpace(localRoot), completeRemote: completeRemote}
	for _, mapping := range mappings {
		local := strings.TrimSpace(mapping.LocalPath)
		if local == "" {
			local = "."
		}
		editor.rows = append(editor.rows, newMappingInputRow(local, strings.TrimSpace(mapping.RemotePath)))
	}
	editor.focus()
	return editor
}

func newMappingInputRow(local, remote string) mappingInputRow {
	localInput := textinput.New()
	localInput.Prompt = ""
	localInput.CharLimit = 1024
	localInput.Placeholder = ". or public/assets"
	localInput.SetValue(local)

	remoteInput := textinput.New()
	remoteInput.Prompt = ""
	remoteInput.CharLimit = 1024
	remoteInput.Placeholder = "/var/www/site"
	remoteInput.SetValue(remote)
	return mappingInputRow{inputs: [2]textinput.Model{localInput, remoteInput}}
}

func (e *mappingEditor) focus() {
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

func (e *mappingEditor) moveRow(delta int) {
	if len(e.rows) == 0 {
		return
	}
	e.cursor = min(max(0, e.cursor+delta), len(e.rows)-1)
	e.popup = nil
	e.focus()
}

func (e *mappingEditor) setColumn(column int) {
	e.column = min(max(0, column), 1)
	e.popup = nil
	e.focus()
}

func (e *mappingEditor) addRow() {
	row := newMappingInputRow("", "")
	insertAt := min(e.cursor+1, len(e.rows))
	e.rows = append(e.rows, mappingInputRow{})
	copy(e.rows[insertAt+1:], e.rows[insertAt:])
	e.rows[insertAt] = row
	e.cursor, e.column = insertAt, 0
	e.popup = nil
	e.status = "mapping added"
	e.focus()
}

func (e *mappingEditor) deleteRow() {
	if len(e.rows) <= 1 {
		e.rows = nil
		e.cursor, e.column = 0, 0
	} else {
		e.rows = append(e.rows[:e.cursor], e.rows[e.cursor+1:]...)
		e.cursor = min(e.cursor, len(e.rows)-1)
	}
	e.popup = nil
	e.status = "mapping removed"
	e.focus()
}

func (e *mappingEditor) reorder(delta int) {
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

func (e *mappingEditor) currentValue() string {
	if len(e.rows) == 0 {
		return ""
	}
	return e.rows[e.cursor].inputs[e.column].Value()
}

func (e *mappingEditor) setCurrentValue(value string) {
	if len(e.rows) == 0 {
		column := e.column
		e.addRow()
		e.setColumn(column)
	}
	e.rows[e.cursor].inputs[e.column].SetValue(value)
	e.rows[e.cursor].inputs[e.column].CursorEnd()
}

func (e *mappingEditor) Update(msg tea.Msg) (tea.Cmd, error) {
	switch msg := msg.(type) {
	case mappingCompletionMsg:
		e.busy, e.popupErr = "", ""
		if msg.err != nil {
			e.popupErr = msg.err.Error()
			e.popup = nil
			return nil, nil
		}
		if msg.row != e.cursor || msg.column != e.column {
			return nil, nil
		}
		switch len(msg.candidates) {
		case 0:
			e.status = "no matches"
		case 1:
			e.setCurrentValue(msg.candidates[0])
			e.status = ""
		default:
			e.popup = msg.candidates
			e.popupAt = 0
		}
		return nil, nil
	case tea.KeyMsg:
		if e.busy != "" {
			return nil, nil
		}
		return e.handleKey(msg)
	}
	return nil, nil
}

func (e *mappingEditor) handleKey(msg tea.KeyMsg) (tea.Cmd, error) {
	key := msg.String()
	if e.popup != nil {
		switch key {
		case "tab", "down", "j", "right":
			e.popupAt = (e.popupAt + 1) % len(e.popup)
		case "shift+tab", "up", "k", "left":
			e.popupAt = (e.popupAt - 1 + len(e.popup)) % len(e.popup)
		case "pgdown":
			e.popupAt = min(len(e.popup)-1, e.popupAt+8)
		case "pgup":
			e.popupAt = max(0, e.popupAt-8)
		case "home":
			e.popupAt = 0
		case "end":
			e.popupAt = len(e.popup) - 1
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
		return nil, errMappingsCancel
	case "enter", "ctrl+s":
		mappings, err := e.validMappings()
		if err != nil {
			e.status = err.Error()
			return nil, nil
		}
		e.result = mappings
		return nil, errMappingsSaved
	case "tab":
		return e.completeCurrent(), nil
	case "shift+tab":
		e.setColumn(1 - e.column)
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
		e.setColumn(1)
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

func (e *mappingEditor) typeInto(msg tea.KeyMsg) (tea.Cmd, error) {
	if len(e.rows) == 0 {
		column := e.column
		e.addRow()
		e.setColumn(column)
	}
	input := &e.rows[e.cursor].inputs[e.column]
	updated, cmd := input.Update(msg)
	*input = updated
	e.popupErr, e.status = "", ""
	return cmd, nil
}

func (e *mappingEditor) completeCurrent() tea.Cmd {
	if len(e.rows) == 0 {
		e.status = "press Ctrl+N to add a mapping"
		return nil
	}
	row, column, value := e.cursor, e.column, e.currentValue()
	e.popup, e.popupErr = nil, ""
	if column == 0 {
		e.busy = "completing local folders…"
		root := e.localRoot
		return func() tea.Msg {
			candidates, err := completeMappingLocal(root, value)
			return mappingCompletionMsg{row: row, column: column, candidates: candidates, err: err}
		}
	}
	e.busy = "completing remote folders…"
	complete := e.completeRemote
	return func() tea.Msg {
		if complete == nil {
			return mappingCompletionMsg{row: row, column: column, err: errors.New("no remote account selected")}
		}
		candidates, err := complete(value)
		return mappingCompletionMsg{row: row, column: column, candidates: candidates, err: err}
	}
}

func completeMappingLocal(localRoot, value string) ([]string, error) {
	root := config.ExpandPath(strings.TrimSpace(localRoot))
	if root == "" {
		return nil, errors.New("set Local root before editing mappings")
	}
	relValue := strings.TrimSpace(value)
	if relValue == "." {
		relValue = ""
	}
	osRelative := filepath.FromSlash(relValue)
	cleanRelative := filepath.Clean(osRelative)
	if filepath.IsAbs(osRelative) || cleanRelative == ".." || strings.HasPrefix(cleanRelative, ".."+string(filepath.Separator)) {
		return nil, errors.New("local mapping paths must stay inside Local root")
	}
	query := filepath.Join(root, osRelative)
	if relValue == "" || strings.HasSuffix(value, "/") || strings.HasSuffix(value, `\`) {
		query += string(filepath.Separator)
	}
	candidates, err := pathcomp.LocalPath(query, true)
	if err != nil {
		return nil, err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidateAbs, err := filepath.Abs(strings.TrimRight(candidate, `/\`))
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(rootAbs, candidateAbs)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		result = append(result, filepath.ToSlash(relative)+"/")
	}
	return result, nil
}

func (e *mappingEditor) validMappings() ([]config.PathMapping, error) {
	mappings := make([]config.PathMapping, 0, len(e.rows))
	for _, row := range e.rows {
		local := strings.TrimSpace(row.inputs[0].Value())
		remote := strings.TrimSpace(row.inputs[1].Value())
		if local == "" && remote == "" {
			continue
		}
		if local == "." {
			local = ""
		}
		mappings = append(mappings, config.PathMapping{LocalPath: local, RemotePath: remote})
	}
	if len(mappings) == 0 {
		return nil, nil
	}
	project := config.Project{LocalRoot: e.localRoot, PathMappings: mappings}
	if err := project.ValidatePathMappings(); err != nil {
		return nil, err
	}
	return mappings, nil
}

func (e *mappingEditor) renderPopup(width, maxShow int) string {
	maxShow = max(3, maxShow)
	visibleCandidates := maxShow
	if len(e.popup) > maxShow {
		visibleCandidates-- // reserve one line for the range indicator
	}
	start := max(0, e.popupAt-visibleCandidates/2)
	start = min(start, max(0, len(e.popup)-visibleCandidates))
	end := min(len(e.popup), start+visibleCandidates)
	rows := make([]string, 0, maxShow)
	for i := start; i < end; i++ {
		candidate := e.popup[i]
		marker := "  "
		if i == e.popupAt {
			marker = "▸ "
		}
		rows = append(rows, marker+truncate(candidate, width-4))
	}
	if len(e.popup) > visibleCandidates {
		rows = append(rows, faintStyle.Render(fmt.Sprintf("  showing %d-%d of %d", start+1, end, len(e.popup))))
	}
	return lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Width(max(20, width)).Padding(0, 1).Render(strings.Join(rows, "\n"))
}

func (e *mappingEditor) View(width, height int) string {
	boxWidth := min(110, max(60, width-4))
	boxHeight := max(8, height-4)
	contentWidth := boxWidth - 4
	separator := " │ "
	prefixWidth := 5
	columnsWidth := contentWidth - prefixWidth - lipgloss.Width(separator)
	localWidth := columnsWidth / 2
	remoteWidth := columnsWidth - localWidth

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
		titleStyle.Render(" Project path mappings"),
		faintStyle.Render(" Local paths are relative to: " + e.localRoot),
		fmt.Sprintf("%-*s%s%-*s", prefixWidth+localWidth, "    LOCAL", separator, remoteWidth, "REMOTE"),
	}
	if len(e.rows) == 0 {
		rows = append(rows, faintStyle.Render("     No path mappings — Default remote will be used."))
	}
	for i := e.offset; i < end; i++ {
		e.rows[i].inputs[0].Width = max(8, localWidth)
		e.rows[i].inputs[1].Width = max(8, remoteWidth)
		marker := "  "
		if i == e.cursor {
			marker = "▸ "
		}
		prefix := fmt.Sprintf("%s%2d ", marker, i+1)
		line := prefix + truncate(e.rows[i].inputs[0].View(), localWidth) + faintStyle.Render(separator) + truncate(e.rows[i].inputs[1].View(), remoteWidth)
		rows = append(rows, truncate(line, contentWidth))
		if i == e.cursor {
			if e.popupErr != "" && e.popup == nil {
				rows = append(rows, errorStyle.Render(strings.Repeat(" ", prefixWidth)+truncate(e.popupErr, contentWidth-prefixWidth)))
			}
			if e.popup != nil {
				rows = append(rows, strings.Repeat(" ", prefixWidth)+e.renderPopup(contentWidth-prefixWidth-4, popupRows))
			}
		}
	}
	if e.offset > 0 || end < len(e.rows) {
		rows = append(rows, faintStyle.Render(fmt.Sprintf(" showing %d-%d of %d mappings", e.offset+1, end, len(e.rows))))
	}
	help := "↑/↓ row · Ctrl+←/→ column · Tab complete · Ctrl+N add · Ctrl+D remove · Ctrl+↑/↓ reorder"
	footer := []string{faintStyle.Render(truncate(help, contentWidth)), faintStyle.Render("Enter apply · Esc cancel")}
	if e.busy != "" {
		footer = append(footer, accentStyle.Render(e.busy))
	} else if e.status != "" {
		style := faintStyle
		if strings.Contains(e.status, "required") || strings.Contains(e.status, "overlap") || strings.Contains(e.status, "must") {
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

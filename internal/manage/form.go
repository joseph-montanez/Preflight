package manage

import (
	"errors"
	"fmt"
	"strings"

	"preflight/internal/listwin"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	errFormSaved  = errors.New("form saved")
	errFormCancel = errors.New("form cancelled")
)

type FieldKind int

const (
	KindText FieldKind = iota
	KindSelect
	KindBool
	KindPath   // local directory path (Tab autocompletes directories)
	KindFile   // local file path (Tab autocompletes files and directories)
	KindRemote // remote directory path (Tab autocompletes over the live connection)
	KindEnv    // environment variable name (Tab autocompletes names)
	KindSecret // masked input, never persisted
	KindEditor // opens a dedicated editor managed by the parent model
)

type Field struct {
	Tag         string
	Kind        FieldKind
	Label       string
	Value       string
	Options     []string
	Placeholder string
	Hint        string
	Complete    func(string) ([]string, error)
	OnChange    func(*Form, int)
	Display     func(string) string
}

func (f *Field) display() string {
	if f.Display != nil {
		return f.Display(f.Value)
	}
	if f.Kind == KindBool {
		if f.Value == "1" {
			return "on"
		}
		return "off"
	}
	return f.Value
}

type completionMsg struct {
	idx        int
	candidates []string
	err        error
}

type testResultMsg struct{ text string }

// Form is a small modal field editor: text-like fields get a textinput,
// select/bool fields are cycled with arrows/space, path-like fields
// autocomplete on Tab, and an optional test action runs async work
// (connection test) with a busy status.
type Form struct {
	Title  string
	Fields []Field
	Cursor int

	onSave func(*Form) error
	onTest func(*Form) tea.Cmd
	// onSecretClear handles Ctrl+X on a KindSecret field (e.g. removing a
	// stored password) and returns nothing; the form stays open.
	onSecretClear func(*Form)

	inputs   []*textinput.Model
	popup    []string
	popupAt  int
	popupErr string
	busy     string
	status   string
}

func NewForm(title string, fields []Field, onSave func(*Form) error, onTest func(*Form) tea.Cmd) *Form {
	f := &Form{Title: title, Fields: fields, onSave: onSave, onTest: onTest}
	for i := range f.Fields {
		kind := f.Fields[i].Kind
		switch kind {
		case KindText, KindPath, KindFile, KindRemote, KindEnv, KindSecret:
			in := textinput.New()
			in.Prompt = ""
			in.CharLimit = 1024
			in.Placeholder = f.Fields[i].Placeholder
			if kind == KindSecret {
				in.EchoMode = textinput.EchoPassword
			}
			in.SetValue(f.Fields[i].Value)
			f.inputs = append(f.inputs, &in)
		default:
			f.inputs = append(f.inputs, nil)
		}
	}
	f.focus()
	return f
}

func (f *Form) byTag(tag string) *Field {
	for i := range f.Fields {
		if f.Fields[i].Tag == tag {
			return &f.Fields[i]
		}
	}
	return nil
}

func (f *Form) byTagIdx(tag string) int {
	for i := range f.Fields {
		if f.Fields[i].Tag == tag {
			return i
		}
	}
	return -1
}

func (f *Form) setValue(i int, v string) {
	f.Fields[i].Value = v
	if in := f.inputs[i]; in != nil {
		in.SetValue(v)
	}
}

func (f *Form) focus() {
	for i := range f.inputs {
		if f.inputs[i] != nil {
			if i == f.Cursor {
				f.inputs[i].Focus()
			} else {
				f.inputs[i].Blur()
			}
		}
	}
}

func (f *Form) move(delta int) {
	n := len(f.Fields)
	if n == 0 {
		return
	}
	f.Cursor = (f.Cursor + delta + n) % n
	f.focus()
}

func (f *Form) fireChange(idx int) {
	if f.Fields[idx].OnChange != nil {
		f.Fields[idx].OnChange(f, idx)
	}
}

func (f *Form) Update(msg tea.Msg) (tea.Cmd, error) {
	switch msg := msg.(type) {
	case completionMsg:
		f.busy = ""
		f.popupErr = ""
		if msg.err != nil {
			f.popupErr = msg.err.Error()
			f.popup = nil
			return nil, nil
		}
		switch len(msg.candidates) {
		case 0:
			f.status = "no matches"
			return nil, nil
		case 1:
			f.setValue(msg.idx, msg.candidates[0])
			f.fireChange(msg.idx)
			f.status = ""
			return nil, nil
		default:
			f.popup = msg.candidates
			f.popupAt = 0
			return nil, nil
		}
	case testResultMsg:
		f.busy = ""
		f.status = msg.text
		return nil, nil
	case tea.KeyMsg:
		if f.busy != "" {
			return nil, nil // ignore keys while testing/completing
		}
		return f.handleKey(msg)
	}
	return nil, nil
}

func (f *Form) handleKey(msg tea.KeyMsg) (tea.Cmd, error) {
	key := msg.String()

	if f.popup != nil {
		switch key {
		case "tab", "down", "j", "right":
			f.popupAt = (f.popupAt + 1) % len(f.popup)
		case "shift+tab", "up", "k", "left":
			f.popupAt = (f.popupAt - 1 + len(f.popup)) % len(f.popup)
		case "enter":
			f.setValue(f.Cursor, f.popup[f.popupAt])
			f.fireChange(f.Cursor)
			f.popup = nil
		case "esc":
			f.popup = nil
			f.status = ""
		default:
			f.popup = nil
			return f.typeInto(msg)
		}
		return nil, nil
	}

	field := &f.Fields[f.Cursor]
	switch key {
	case "ctrl+c", "esc":
		return nil, errFormCancel
	case "enter":
		if field.Kind == KindSelect && len(field.Options) > 4 {
			f.popup = append([]string{}, field.Options...)
			f.popupAt = indexOf(field.Options, field.Value)
			if f.popupAt < 0 {
				f.popupAt = 0
			}
			return nil, nil
		}
		if f.onSave != nil {
			if err := f.onSave(f); err != nil {
				f.status = err.Error()
				return nil, nil
			}
			return nil, errFormSaved
		}
		return nil, errFormCancel
	case "tab":
		if isPathLike(field) && field.Complete != nil {
			f.busy = "completing…"
			idx := f.Cursor
			value := field.Value
			return func() tea.Msg {
				cands, err := field.Complete(value)
				return completionMsg{idx: idx, candidates: cands, err: err}
			}, nil
		}
		f.move(1)
		return nil, nil
	case "shift+tab":
		f.move(-1)
		return nil, nil
	case "up":
		f.move(-1)
		return nil, nil
	case "down":
		f.move(1)
		return nil, nil
	case "k":
		// j/k navigate only where typing is meaningless (select/bool);
		// on text fields they are ordinary letters.
		if isTypable(field) {
			return f.typeInto(msg)
		}
		f.move(-1)
		return nil, nil
	case "j":
		if isTypable(field) {
			return f.typeInto(msg)
		}
		f.move(1)
		return nil, nil
	case "left", "right", " ":
		if field.Kind == KindBool {
			if field.Value == "1" {
				f.setValue(f.Cursor, "0")
			} else {
				f.setValue(f.Cursor, "1")
			}
			f.fireChange(f.Cursor)
			return nil, nil
		}
		if field.Kind == KindSelect {
			delta := 1
			if key == "left" {
				delta = -1
			}
			f.cycleSelect(delta)
			return nil, nil
		}
	case "ctrl+t":
		if f.onTest != nil {
			f.busy = "testing connection…"
			return f.onTest(f), nil
		}
	case "ctrl+x":
		if field.Kind == KindSecret && f.onSecretClear != nil {
			f.onSecretClear(f)
			return nil, nil
		}
	}
	return f.typeInto(msg)
}

func (f *Form) cycleSelect(delta int) {
	field := &f.Fields[f.Cursor]
	if len(field.Options) == 0 {
		return
	}
	cur := indexOf(field.Options, field.Value)
	if cur < 0 {
		cur = 0
	}
	cur = (cur + delta + len(field.Options)) % len(field.Options)
	f.setValue(f.Cursor, field.Options[cur])
	f.fireChange(f.Cursor)
}

func (f *Form) typeInto(msg tea.KeyMsg) (tea.Cmd, error) {
	in := f.inputs[f.Cursor]
	if in == nil {
		return nil, nil
	}
	updated, cmd := in.Update(msg)
	f.inputs[f.Cursor] = &updated
	f.Fields[f.Cursor].Value = updated.Value()
	return cmd, nil
}

func isPathLike(field *Field) bool {
	switch field.Kind {
	case KindPath, KindFile, KindRemote, KindEnv:
		return true
	}
	return false
}

func isTypable(field *Field) bool {
	switch field.Kind {
	case KindText, KindPath, KindFile, KindRemote, KindEnv, KindSecret:
		return true
	}
	return false
}

func indexOf(list []string, v string) int {
	for i, s := range list {
		if s == v {
			return i
		}
	}
	return -1
}

func (f *Form) renderField(i, w, labelW int) string {
	field := &f.Fields[i]
	label := field.Label
	if lipgloss.Width(label) > labelW {
		label = truncate(label, labelW)
	}
	label = fmt.Sprintf("%-*s", labelW, label)
	var value string
	if in := f.inputs[i]; in != nil {
		in.Width = max(10, w-labelW-4)
		value = in.View()
	} else {
		switch field.Kind {
		case KindSelect:
			value = "‹ " + field.display() + " ›"
		case KindBool:
			value = "[ " + field.display() + " ]"
		default:
			value = field.display()
		}
	}
	row := mutedStyle.Render(label) + " " + value
	if i == f.Cursor {
		row = "▸ " + row
	}
	return truncate(row, w)
}

func (f *Form) renderPopup(w int) string {
	const maxShow = 6
	rows := make([]string, 0, min(len(f.popup), maxShow)+1)
	for i, cand := range f.popup {
		if i >= maxShow {
			rows = append(rows, faintStyle.Render(fmt.Sprintf("  … %d more", len(f.popup)-maxShow)))
			break
		}
		marker := "  "
		if i == f.popupAt {
			marker = "▸ "
		}
		rows = append(rows, marker+truncate(cand, w-4))
	}
	return lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(0, 1).Render(strings.Join(rows, "\n"))
}

func (f *Form) View(width, height int) string {
	boxW := min(80, max(44, width-4))
	contentW := boxW - 4 // border + padding
	labelW := 18
	maxRows := max(8, height-2)

	tail := []string{helpRow(f, contentW-2), statusRow(f, contentW-2)}
	// The title, the help row and the status row are always visible, and the
	// completion popup hangs off the cursor field; whatever is left is the
	// budget for the field window.
	reserved := 1 + len(tail) + popupLineCount(f)
	start, end, moreAbove, moreBelow := listwin.Window(len(f.Fields), f.Cursor, max(1, maxRows-reserved))

	rows := []string{titleStyle.Render(" " + f.Title)}
	if moreAbove {
		rows = append(rows, faintStyle.Render(fmt.Sprintf(" ↑ %d more", start)))
	}
	for i := start; i < end; i++ {
		rows = append(rows, f.renderField(i, contentW-1, labelW))
		if i == f.Cursor {
			if f.popupErr != "" && f.popup == nil {
				rows = append(rows, errorStyle.Render(" "+f.popupErr))
			}
			if f.popup != nil {
				rows = append(rows, f.renderPopup(contentW-2))
			}
		}
	}
	if moreBelow {
		rows = append(rows, faintStyle.Render(fmt.Sprintf(" ↓ %d more", len(f.Fields)-end)))
	}
	rows = append(rows, tail...)
	// Safety net: an oversized popup can still overshoot the budget.
	if len(rows) > maxRows {
		rows = append(rows[:maxRows-1], faintStyle.Render(" …"))
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Width(boxW).Padding(0, 1)
	return box.Render(strings.Join(rows, "\n"))
}

// popupLineCount is how many rows the completion popup (or its error line)
// adds below the cursor field.
func popupLineCount(f *Form) int {
	if f.popup == nil {
		if f.popupErr != "" {
			return 1
		}
		return 0
	}
	const maxShow = 6
	lines := min(len(f.popup), maxShow)
	if len(f.popup) > maxShow {
		lines++ // the "… n more" line
	}
	return lines + 2 // border
}

func helpRow(f *Form, w int) string {
	enterHint := "Enter save"
	if f.Fields[f.Cursor].Kind == KindEditor {
		enterHint = "Enter edit"
	}
	parts := []string{"↑/↓ move", enterHint, "Esc cancel"}
	for _, field := range f.Fields {
		if isPathLike(&field) && field.Complete != nil {
			parts = append(parts, "Tab complete")
			break
		}
	}
	for _, field := range f.Fields {
		if field.Kind == KindSelect {
			parts = append(parts, "←/→ cycle", "Enter pick")
			break
		}
	}
	if f.onTest != nil {
		parts = append(parts, "Ctrl+T test")
	}
	return faintStyle.Render(strings.Join(parts, " · "))
}

func statusRow(f *Form, w int) string {
	text := f.status
	if f.busy != "" {
		text = accentStyle.Render(f.busy)
	} else if text == "" && f.Fields[f.Cursor].Hint != "" {
		text = f.Fields[f.Cursor].Hint
	}
	if text == "" {
		return ""
	}
	return truncate(text, w)
}

func truncate(value string, width int) string {
	if width <= 0 || lipgloss.Width(value) <= width {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 && lipgloss.Width(string(runes)+"…") > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

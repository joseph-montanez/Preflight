package manage

import (
	"fmt"
	"strings"

	"preflight/internal/config"
)

const (
	tagOnError = "on_error"
	tagSteps   = "steps"
)

// openTaskForm opens the task editor; idx == -1 creates a new one. Steps
// are edited in the dedicated step editor (Enter on the Steps row) and
// kept in stepsPtr so the form save sees the applied rows.
func (m *Model) openTaskForm(idx int) {
	task := config.Task{}
	if idx >= 0 && idx < len(m.cfg.Tasks) {
		task = m.cfg.Tasks[idx]
	}
	title := "New Task"
	if idx >= 0 {
		title = "Task: " + task.ID
	}
	if task.OnError == "" {
		task.OnError = "stop"
	}
	steps := append([]config.TaskStep(nil), task.Steps...)
	stepsPtr := &steps

	fields := []Field{
		{Tag: tagID, Kind: KindText, Label: "ID", Value: task.ID, Placeholder: "deploy-apiv2"},
		{Tag: tagName, Kind: KindText, Label: "Name", Value: task.Name, Placeholder: "Build apiv2 and restart"},
		{Tag: tagOnError, Kind: KindSelect, Label: "On error", Value: task.OnError, Options: []string{"stop", "continue"}, Hint: "stop aborts later steps"},
		{Tag: tagSteps, Kind: KindEditor, Label: "Steps", Hint: "Enter to edit commands", Display: func(string) string {
			return stepSummary(*stepsPtr)
		}},
	}
	form := NewForm(title, fields, func(f *Form) error {
		t, err := taskFromForm(f, *stepsPtr)
		if err != nil {
			return err
		}
		for i, existing := range m.cfg.Tasks {
			if existing.ID == t.ID && i != idx {
				return fmt.Errorf("task %q already exists", t.ID)
			}
		}
		// Validate on a copy first so a rejected save leaves the
		// config untouched. This catches unknown or shell-less
		// accounts in steps.
		applied := append([]config.Task(nil), m.cfg.Tasks...)
		if idx >= 0 && idx < len(applied) {
			applied[idx] = t
		} else {
			applied = append(applied, t)
		}
		probe := *m.cfg
		probe.Tasks = applied
		if err := probe.ValidateTasks(); err != nil {
			return err
		}
		m.cfg.Tasks = applied
		return nil
	}, nil)
	m.form = form
	m.stepsTarget = stepsPtr
}

// openStepsEditor opens the step editor for the task form's Steps row.
func (m *Model) openStepsEditor() {
	if m.form == nil || m.stepsTarget == nil {
		return
	}
	accounts := make([]string, 0, len(m.cfg.Accounts))
	for _, a := range m.cfg.Accounts {
		if strings.EqualFold(a.Protocol, "sftp") {
			accounts = append(accounts, a.ID)
		}
	}
	m.steps = newStepEditor(*m.stepsTarget, accounts)
}

func stepSummary(steps []config.TaskStep) string {
	switch len(steps) {
	case 0:
		return "(none)"
	case 1:
		return "1 step"
	default:
		return fmt.Sprintf("%d steps", len(steps))
	}
}

func taskFromForm(f *Form, steps []config.TaskStep) (config.Task, error) {
	id := strings.TrimSpace(f.byTag(tagID).Value)
	if id == "" {
		return config.Task{}, fmt.Errorf("ID is required")
	}
	if len(steps) == 0 {
		return config.Task{}, fmt.Errorf("at least one step is required")
	}
	return config.Task{
		ID:      id,
		Name:    strings.TrimSpace(f.byTag(tagName).Value),
		OnError: f.byTag(tagOnError).Value,
		Steps:   steps,
	}, nil
}

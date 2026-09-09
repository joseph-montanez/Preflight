package manage

import (
	"fmt"
	"strings"

	"preflight/internal/config"
	"preflight/internal/pathcomp"
)

const (
	tagAccount       = "account"
	tagLocalRoot     = "local_root"
	tagRemoteRoot    = "remote_root"
	tagMappings      = "path_mappings"
	tagDiff          = "diff"
	tagIgnoreWS      = "ignore_ws"
	tagIgnoreBlanks  = "ignore_blanks"
	tagStripComments = "strip_comments"
	tagExclusions    = "exclusions"
	tagGitStatus     = "git_status"
)

// openProjectForm opens the project editor; idx == -1 creates a new one.
func (m *Model) openProjectForm(idx int) {
	proj := config.Project{}
	if idx >= 0 && idx < len(m.cfg.Projects) {
		proj = m.cfg.Projects[idx]
	}
	title := "New Project"
	if idx >= 0 {
		title = "Project: " + proj.ID
	}

	accountOptions := make([]string, 0, len(m.cfg.Accounts))
	for _, a := range m.cfg.Accounts {
		accountOptions = append(accountOptions, a.ID)
	}
	if proj.AccountID == "" && len(accountOptions) > 0 {
		proj.AccountID = accountOptions[0]
	}
	if proj.DefaultDiffMode == "" {
		proj.DefaultDiffMode = "text"
	}
	exclusions := strings.Join(proj.Exclusions, ", ")
	mappings := formatMappings(proj.PathMappings)

	var form *Form
	fields := []Field{
		{Tag: tagID, Kind: KindText, Label: "ID", Value: proj.ID, Placeholder: "api-backend"},
		{Tag: tagName, Kind: KindText, Label: "Name", Value: proj.Name, Placeholder: "Backend API"},
		{Tag: tagAccount, Kind: KindSelect, Label: "Account", Value: proj.AccountID, Options: accountOptions, Hint: "Enter to pick"},
		{Tag: tagLocalRoot, Kind: KindPath, Label: "Local root", Value: proj.LocalRoot, Placeholder: "/Users/dev/code/api", Hint: "Tab to complete", Complete: func(v string) ([]string, error) {
			return pathcomp.LocalPath(v, true)
		}},
		{Tag: tagRemoteRoot, Kind: KindRemote, Label: "Default remote", Value: proj.RemoteRoot, Placeholder: "/var/www/api", Hint: "used when mappings are empty; Tab to browse", Complete: func(v string) ([]string, error) {
			acc, err := m.cfg.Account(form.byTag(tagAccount).Value)
			if err != nil {
				return nil, err
			}
			return m.completer.complete(acc, v)
		}},
		{Tag: tagDiff, Kind: KindSelect, Label: "Diff mode", Value: proj.DefaultDiffMode, Options: []string{"text", "binary", "size", "modtime"}},
		{Tag: tagIgnoreWS, Kind: KindBool, Label: "Ignore whitespace", Value: boolVal(proj.TextDiffOptions.IgnoreWhitespace)},
		{Tag: tagIgnoreBlanks, Kind: KindBool, Label: "Ignore blank lines", Value: boolVal(proj.TextDiffOptions.IgnoreBlankLines)},
		{Tag: tagStripComments, Kind: KindBool, Label: "Strip comments", Value: boolVal(proj.TextDiffOptions.StripComments)},
		{Tag: tagExclusions, Kind: KindText, Label: "Exclusions", Value: exclusions, Placeholder: ".git/, node_modules/, vendor/", Hint: "comma or newline separated"},
		{Tag: tagGitStatus, Kind: KindBool, Label: "Git status sync", Value: boolVal(proj.GitStatus), Hint: "pre-mark git changes in sync"},
		{Tag: tagMappings, Kind: KindEditor, Label: "Path mappings", Value: mappings, Hint: "Enter to edit multiple local/remote mappings", Display: mappingSummary},
	}
	form = NewForm(title, fields, func(f *Form) error {
		p, err := projectFromForm(f)
		if err != nil {
			return err
		}
		if strings.TrimSpace(p.ID) == "" {
			return fmt.Errorf("ID is required")
		}
		if strings.TrimSpace(p.LocalRoot) == "" {
			return fmt.Errorf("local_root is required")
		}
		if err := p.ValidatePathMappings(); err != nil {
			return err
		}
		if _, err := m.cfg.Account(p.AccountID); err != nil {
			return fmt.Errorf("no matching account — create one in the Accounts tab first")
		}
		for i, existing := range m.cfg.Projects {
			if existing.ID == p.ID && i != idx {
				return fmt.Errorf("project %q already exists", p.ID)
			}
		}
		if idx >= 0 && idx < len(m.cfg.Projects) {
			m.cfg.Projects[idx] = p
		} else {
			m.cfg.Projects = append(m.cfg.Projects, p)
		}
		return nil
	}, nil)
	m.form = form
}

func (m *Model) openMappingEditor() {
	if m.form == nil {
		return
	}
	mappings, err := parseMappings(m.form.byTag(tagMappings).Value)
	if err != nil {
		m.form.status = err.Error()
		return
	}
	localRoot := m.form.byTag(tagLocalRoot).Value
	completeRemote := func(value string) ([]string, error) {
		account, err := m.cfg.Account(m.form.byTag(tagAccount).Value)
		if err != nil {
			return nil, err
		}
		return m.completer.complete(account, value)
	}
	m.mappings = newMappingEditor(localRoot, mappings, completeRemote)
}

func mappingSummary(value string) string {
	mappings, err := parseMappings(value)
	if err != nil {
		return value
	}
	switch len(mappings) {
	case 0:
		return "(none — uses Default remote)"
	case 1:
		local := mappings[0].LocalPath
		if local == "" {
			local = "."
		}
		return local + " → " + mappings[0].RemotePath
	default:
		return fmt.Sprintf("%d mappings", len(mappings))
	}
}

func projectFromForm(f *Form) (config.Project, error) {
	mappings, err := parseMappings(f.byTag(tagMappings).Value)
	if err != nil {
		return config.Project{}, err
	}
	remoteRoot := strings.TrimSpace(f.byTag(tagRemoteRoot).Value)
	if len(mappings) > 0 {
		remoteRoot = ""
	}
	return config.Project{
		ID:              strings.TrimSpace(f.byTag(tagID).Value),
		Name:            strings.TrimSpace(f.byTag(tagName).Value),
		AccountID:       f.byTag(tagAccount).Value,
		LocalRoot:       strings.TrimSpace(f.byTag(tagLocalRoot).Value),
		RemoteRoot:      remoteRoot,
		PathMappings:    mappings,
		DefaultDiffMode: f.byTag(tagDiff).Value,
		TextDiffOptions: config.TextDiffOptions{
			IgnoreWhitespace: f.byTag(tagIgnoreWS).Value == "1",
			IgnoreBlankLines: f.byTag(tagIgnoreBlanks).Value == "1",
			StripComments:    f.byTag(tagStripComments).Value == "1",
		},
		Exclusions: splitList(f.byTag(tagExclusions).Value),
		GitStatus:  f.byTag(tagGitStatus).Value == "1",
	}, nil
}

func formatMappings(mappings []config.PathMapping) string {
	parts := make([]string, 0, len(mappings))
	for _, mapping := range mappings {
		localPath := strings.TrimSpace(mapping.LocalPath)
		if localPath == "" {
			localPath = "."
		}
		parts = append(parts, localPath+" => "+strings.TrimSpace(mapping.RemotePath))
	}
	return strings.Join(parts, "; ")
}

func parseMappings(value string) ([]config.PathMapping, error) {
	var mappings []config.PathMapping
	for _, entry := range strings.FieldsFunc(value, func(r rune) bool { return r == ';' || r == '\n' }) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		localPath, remotePath, ok := strings.Cut(entry, "=>")
		if !ok {
			return nil, fmt.Errorf("invalid path mapping %q (use local => remote)", entry)
		}
		localPath = strings.TrimSpace(localPath)
		if localPath == "." {
			localPath = ""
		}
		mappings = append(mappings, config.PathMapping{LocalPath: localPath, RemotePath: strings.TrimSpace(remotePath)})
	}
	return mappings, nil
}

func splitList(v string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == '\n'
	}) {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

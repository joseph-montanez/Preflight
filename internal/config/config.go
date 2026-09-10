package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Accounts []Account `yaml:"accounts"`
	Projects []Project `yaml:"projects"`
	// Tasks are named ordered sets of remote commands (run configurations).
	// Absent means no tasks are defined.
	Tasks []Task `yaml:"tasks,omitempty"`
	// Editor is the terminal editor command line launched by the [E] editor
	// keys in the browsers (e.g. "hx" for helix). Empty means the editor
	// keys only show a status hint. GUI editors that detach need a wait
	// flag (e.g. "code --wait").
	Editor string `yaml:"editor"`
}

type Account struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	Protocol string `yaml:"protocol"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	TLSMode  string `yaml:"tls_mode,omitempty"`
	Auth     Auth   `yaml:"auth"`
}

type Auth struct {
	Type             string `yaml:"type"`
	Username         string `yaml:"username"`
	KeyPath          string `yaml:"key_path,omitempty"`
	PassphrasePrompt bool   `yaml:"passphrase_prompt,omitempty"`
	PasswordEnv      string `yaml:"password_env,omitempty"`
}

type Project struct {
	ID              string          `yaml:"id"`
	Name            string          `yaml:"name"`
	AccountID       string          `yaml:"account_id"`
	LocalRoot       string          `yaml:"local_root"`
	RemoteRoot      string          `yaml:"remote_root,omitempty"`
	PathMappings    []PathMapping   `yaml:"path_mappings,omitempty"`
	DefaultDiffMode string          `yaml:"default_diff_mode"`
	TextDiffOptions TextDiffOptions `yaml:"text_diff_options"`
	Exclusions      []string        `yaml:"exclusions"`
	GitStatus       bool            `yaml:"git_status,omitempty"`
}

// PathMapping links a project-relative local folder to an independent
// remote directory. LocalPath may be "." (or empty in memory) for the
// project root.
type PathMapping struct {
	LocalPath  string `yaml:"local_path"`
	RemotePath string `yaml:"remote_path"`
}

// EffectivePathMappings returns the configured mappings, or adapts the
// original single remote_root format into a root mapping. Keeping this in
// one place makes older config files continue to work unchanged.
func (p Project) EffectivePathMappings() []PathMapping {
	if len(p.PathMappings) > 0 {
		out := make([]PathMapping, len(p.PathMappings))
		for i, mapping := range p.PathMappings {
			localPath := strings.Trim(strings.ReplaceAll(mapping.LocalPath, "\\", "/"), "/")
			if localPath == "." {
				localPath = ""
			}
			out[i] = PathMapping{LocalPath: localPath, RemotePath: strings.TrimSpace(mapping.RemotePath)}
		}
		return out
	}
	if strings.TrimSpace(p.RemoteRoot) == "" {
		return nil
	}
	return []PathMapping{{RemotePath: strings.TrimSpace(p.RemoteRoot)}}
}

// RemoteSummary is a compact label for project lists and review headers.
func (p Project) RemoteSummary() string {
	mappings := p.EffectivePathMappings()
	if len(mappings) == 0 {
		return "(no remote mapping)"
	}
	if len(mappings) == 1 {
		return mappings[0].RemotePath
	}
	return fmt.Sprintf("%d mapped targets", len(mappings))
}

// ValidatePathMappings checks that every configured local folder is safely
// inside local_root and that mappings do not overlap (which would otherwise
// plan the same file twice when a parent folder is selected).
func (p Project) ValidatePathMappings() error {
	mappings := p.EffectivePathMappings()
	if len(mappings) == 0 {
		return errors.New("at least one remote_root or path_mapping is required")
	}
	for i, mapping := range mappings {
		rawLocal := strings.TrimSpace(mapping.LocalPath)
		if len(p.PathMappings) > 0 {
			rawLocal = strings.TrimSpace(p.PathMappings[i].LocalPath)
		}
		localPath := strings.Trim(strings.ReplaceAll(rawLocal, "\\", "/"), "/")
		if localPath == "." {
			localPath = ""
		}
		if filepath.IsAbs(rawLocal) || localPath == ".." || strings.HasPrefix(localPath, "../") || strings.Contains(localPath, "/../") || strings.HasSuffix(localPath, "/..") {
			return fmt.Errorf("path_mappings[%d].local_path must be relative to local_root", i)
		}
		if strings.TrimSpace(mapping.RemotePath) == "" {
			return fmt.Errorf("path_mappings[%d].remote_path is required", i)
		}
		for j := i + 1; j < len(mappings); j++ {
			other := mappings[j].LocalPath
			if localPath == other || (localPath != "" && other != "" && (strings.HasPrefix(other, localPath+"/") || strings.HasPrefix(localPath, other+"/"))) {
				return fmt.Errorf("overlapping path_mappings: %q and %q", mapping.LocalPath, mappings[j].LocalPath)
			}
		}
	}
	return nil
}

// IsPathCovered reports whether relPath (relative to local_root) is covered by
// at least one of the project's effective path mappings. isDir indicates whether
// relPath represents a directory.
func (p Project) IsPathCovered(relPath string, isDir bool) bool {
	return IsMappingCovered(relPath, isDir, p.EffectivePathMappings())
}

// IsMappingCovered reports whether relPath is covered by mappings.
// If mappings is empty, it returns false.
// If any mapping has LocalPath == "" (root mapping), all paths are covered.
// A file is covered if it lies under or matches a mapping's LocalPath.
// A directory is covered if it lies under a mapping, matches a mapping, or
// is an ancestor directory of a mapping (meaning it contains that mapping).
func IsMappingCovered(relPath string, isDir bool, mappings []PathMapping) bool {
	if len(mappings) == 0 {
		return false
	}
	rel := cleanMappingRel(relPath)
	for _, m := range mappings {
		base := cleanMappingRel(m.LocalPath)
		if base == "" {
			return true // root mapping covers everything in the project
		}
		if base == rel || strings.HasPrefix(rel, base+"/") {
			return true // rel is inside or equals this mapping
		}
		if isDir && (rel == "" || strings.HasPrefix(base, rel+"/")) {
			return true // directory contains this mapping (ancestor of base)
		}
	}
	return false
}

func cleanMappingRel(value string) string {
	value = strings.Trim(strings.ReplaceAll(value, "\\", "/"), "/")
	if value == "." {
		return ""
	}
	return value
}

func pathContains(parent, child string) bool {
	return parent == "" || strings.HasPrefix(child, parent+"/")
}

func displayLocal(value string) string {
	if value == "" {
		return "."
	}
	return value
}

// Task is a named ordered set of remote commands (a run configuration),
// e.g. build on one account then restart a service on another. Steps run
// sequentially in order; a failing step stops the run unless OnError is
// "continue".
type Task struct {
	ID      string     `yaml:"id"`
	Name    string     `yaml:"name"`
	OnError string     `yaml:"on_error,omitempty"`
	Steps   []TaskStep `yaml:"steps"`
}

// TaskStep runs one command on an account, or uploads one file to it.
// A run step sets Run (Workdir is entered with cd before Run, and Shell
// optionally wraps the step in another interpreter, e.g. `bash -lc` for
// a login shell). An upload step sets Upload (local source) and Remote
// (absolute remote destination) instead; exactly one of Run / Upload is
// required, and Workdir/Shell are run-only.
type TaskStep struct {
	AccountID string `yaml:"account_id"`
	Workdir   string `yaml:"workdir,omitempty"`
	Run       string `yaml:"run,omitempty"`
	Shell     string `yaml:"shell,omitempty"`
	Upload    string `yaml:"upload,omitempty"`
	Remote    string `yaml:"remote,omitempty"`
}

// Step kinds. A step is an upload step when Upload is set, otherwise a
// run step. Kind centralizes the discrimination so validation, the
// runner, and the editor agree on it (SRP: shape knowledge lives here).
const (
	TaskStepRun    = "run"
	TaskStepUpload = "upload"
)

// Kind reports the step kind from its set fields.
func (s TaskStep) Kind() string {
	if strings.TrimSpace(s.Upload) != "" {
		return TaskStepUpload
	}
	return TaskStepRun
}

// IsUpload reports whether the step uploads a file.
func (s TaskStep) IsUpload() bool { return s.Kind() == TaskStepUpload }

// Validate checks the step's own shape against its account. Task-level
// concerns (ids, account existence) stay in ValidateTasks; protocol and
// field rules live here so every caller shares them.
func (s TaskStep) Validate(account Account, taskID string, index int) error {
	prefix := fmt.Sprintf("task %q steps[%d]", taskID, index)
	hasRun := strings.TrimSpace(s.Run) != ""
	hasUpload := strings.TrimSpace(s.Upload) != ""
	hasRemote := strings.TrimSpace(s.Remote) != ""
	switch {
	case !hasRun && !hasUpload:
		return fmt.Errorf("%s.run is required", prefix)
	case hasRun && hasUpload:
		return fmt.Errorf("%s: run and upload are mutually exclusive", prefix)
	}
	if hasUpload {
		if !hasRemote {
			return fmt.Errorf("%s.remote is required", prefix)
		}
		if !strings.HasPrefix(strings.TrimSpace(s.Remote), "/") {
			return fmt.Errorf("%s.remote must be an absolute path", prefix)
		}
		if strings.TrimSpace(s.Workdir) != "" || strings.TrimSpace(s.Shell) != "" {
			return fmt.Errorf("%s: workdir/shell apply to run steps only", prefix)
		}
		switch strings.ToLower(strings.TrimSpace(account.Protocol)) {
		case "sftp", "ftp", "ftps":
		default:
			return fmt.Errorf("%s: account %q uses %q, only sftp/ftp/ftps accounts can upload files",
				prefix, account.ID, account.Protocol)
		}
		return nil
	}
	if hasRemote {
		return fmt.Errorf("%s.remote requires upload", prefix)
	}
	if !strings.EqualFold(account.Protocol, "sftp") {
		return fmt.Errorf("%s: account %q uses %q, only sftp accounts can run commands",
			prefix, account.ID, account.Protocol)
	}
	return nil
}

// ContinueOnError reports whether a failing step lets later steps run.
// Empty and "stop" mean stop; "continue" means carry on. Anything else is
// rejected by ValidateTasks.
func (t Task) ContinueOnError() bool {
	return strings.EqualFold(strings.TrimSpace(t.OnError), "continue")
}

// ValidateTasks checks task ids, steps, and referenced accounts. Remote
// execution needs a shell, so only SFTP accounts are accepted.
func (c *Config) ValidateTasks() error {
	seen := map[string]bool{}
	for i := range c.Tasks {
		task := &c.Tasks[i]
		if strings.TrimSpace(task.ID) == "" {
			return fmt.Errorf("tasks[%d].id is required", i)
		}
		if seen[task.ID] {
			return fmt.Errorf("duplicate task id %q", task.ID)
		}
		seen[task.ID] = true
		switch normalized := strings.ToLower(strings.TrimSpace(task.OnError)); normalized {
		case "", "stop", "continue":
		default:
			return fmt.Errorf("task %q on_error must be stop or continue", task.ID)
		}
		if len(task.Steps) == 0 {
			return fmt.Errorf("task %q has no steps", task.ID)
		}
		for j := range task.Steps {
			step := &task.Steps[j]
			account, err := c.Account(step.AccountID)
			if err != nil {
				return fmt.Errorf("task %q steps[%d]: %w", task.ID, j, err)
			}
			if err := step.Validate(*account, task.ID, j); err != nil {
				return err
			}
		}
	}
	return nil
}

type TextDiffOptions struct {
	IgnoreWhitespace bool `yaml:"ignore_whitespace"`
	IgnoreBlankLines bool `yaml:"ignore_blank_lines"`
	StripComments    bool `yaml:"strip_comments"`
}

func DefaultPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "preflight", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "preflight", "config.yaml")
	}
	return filepath.Join(home, ".config", "preflight", "config.yaml")
}

func ExpandPath(value string) string {
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			if value == "~" {
				return home
			}
			return filepath.Join(home, value[2:])
		}
	}
	return value
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(ExpandPath(path))
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	for i := range cfg.Projects {
		cfg.Projects[i].LocalRoot = ExpandPath(cfg.Projects[i].LocalRoot)
	}
	for i := range cfg.Accounts {
		cfg.Accounts[i].Auth.KeyPath = ExpandPath(cfg.Accounts[i].Auth.KeyPath)
		if cfg.Accounts[i].Port == 0 {
			switch strings.ToLower(cfg.Accounts[i].Protocol) {
			case "sftp":
				cfg.Accounts[i].Port = 22
			case "ftp", "ftps":
				cfg.Accounts[i].Port = 21
			}
		}
	}
	return &cfg, nil
}

// Save marshals the config back to YAML and writes it to path,
// creating parent directories as needed.
func (c *Config) Save(path string) error {
	path = ExpandPath(path)
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadOrEmpty loads the config at path, returning an empty config when
// the file does not exist yet (so a manager can create it).
func LoadOrEmpty(path string) (*Config, error) {
	_, err := os.Stat(ExpandPath(path))
	switch {
	case err == nil:
		return Load(path)
	case errors.Is(err, os.ErrNotExist):
		return &Config{}, nil
	default:
		return nil, err
	}
}

func (c *Config) Project(id string) (*Project, error) {
	for i := range c.Projects {
		if c.Projects[i].ID == id {
			return &c.Projects[i], nil
		}
	}
	return nil, fmt.Errorf("project %q not found", id)
}

func (c *Config) Account(id string) (*Account, error) {
	for i := range c.Accounts {
		if c.Accounts[i].ID == id {
			return &c.Accounts[i], nil
		}
	}
	return nil, fmt.Errorf("account %q not found", id)
}

func (c *Config) Task(id string) (*Task, error) {
	for i := range c.Tasks {
		if c.Tasks[i].ID == id {
			return &c.Tasks[i], nil
		}
	}
	return nil, fmt.Errorf("task %q not found", id)
}

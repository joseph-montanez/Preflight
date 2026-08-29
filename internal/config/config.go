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
		for j := 0; j < i; j++ {
			other := mappings[j].LocalPath
			if localPath == other || pathContains(localPath, other) || pathContains(other, localPath) {
				return fmt.Errorf("path_mappings local paths %q and %q overlap", displayLocal(other), displayLocal(localPath))
			}
		}
	}
	return nil
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

type TextDiffOptions struct {
	IgnoreWhitespace bool `yaml:"ignore_whitespace"`
	IgnoreBlankLines bool `yaml:"ignore_blank_lines"`
	StripComments    bool `yaml:"strip_comments"`
}

func DefaultPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "godeploy", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "godeploy", "config.yaml")
	}
	return filepath.Join(home, ".config", "godeploy", "config.yaml")
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

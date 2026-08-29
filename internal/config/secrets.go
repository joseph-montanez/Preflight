package config

import (
	"errors"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Secrets holds per-account credentials that are deliberately kept out of
// config.yaml. It is written to secrets.yaml next to the config file with
// 0600 permissions.
type Secrets struct {
	Accounts map[string]AccountSecret `yaml:"accounts"`
}

type AccountSecret struct {
	Password   string `yaml:"password,omitempty"`
	Passphrase string `yaml:"passphrase,omitempty"`
}

// SecretsPath returns the secrets file path for a given config path.
func SecretsPath(configPath string) string {
	return filepath.Join(filepath.Dir(ExpandPath(configPath)), "secrets.yaml")
}

// LoadSecrets reads the secrets file, returning an empty set when the file
// does not exist yet.
func LoadSecrets(path string) (*Secrets, error) {
	data, err := os.ReadFile(ExpandPath(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Secrets{Accounts: map[string]AccountSecret{}}, nil
		}
		return nil, err
	}
	var s Secrets
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.Accounts == nil {
		s.Accounts = map[string]AccountSecret{}
	}
	return &s, nil
}

// Save writes the secrets file with 0600 permissions, creating parent
// directories as needed.
func (s *Secrets) Save(path string) error {
	path = ExpandPath(path)
	if s.Accounts == nil {
		s.Accounts = map[string]AccountSecret{}
	}
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

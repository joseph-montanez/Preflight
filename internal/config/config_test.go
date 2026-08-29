package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.yaml")
	cfg := &Config{
		Editor: "hx --log /tmp/hx.log",
		Accounts: []Account{{
			ID:       "a1",
			Name:     "A1",
			Protocol: "sftp",
			Host:     "h",
			Port:     22,
			Auth:     Auth{Type: "key", Username: "u", KeyPath: "~/.ssh/id_ed25519", PassphrasePrompt: true},
		}},
		Projects: []Project{{
			ID:              "p1",
			Name:            "P1",
			AccountID:       "a1",
			LocalRoot:       "/x",
			RemoteRoot:      "/y",
			DefaultDiffMode: "text",
			TextDiffOptions: TextDiffOptions{IgnoreWhitespace: true},
			Exclusions:      []string{".git/"},
			PathMappings: []PathMapping{
				{LocalPath: "public", RemotePath: "/var/www/site"},
				{LocalPath: "ops/config", RemotePath: "/etc/site"},
			},
		}},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Accounts) != 1 || got.Accounts[0].ID != "a1" || !got.Accounts[0].Auth.PassphrasePrompt {
		t.Fatalf("account round-trip mismatch: %+v", got.Accounts)
	}
	if got.Editor != "hx --log /tmp/hx.log" {
		t.Fatalf("editor round-trip mismatch: %q", got.Editor)
	}
	if got.Accounts[0].Auth.KeyPath == "~/.ssh/id_ed25519" {
		t.Fatalf("key path should be expanded on load: %q", got.Accounts[0].Auth.KeyPath)
	}
	if len(got.Projects) != 1 || !got.Projects[0].TextDiffOptions.IgnoreWhitespace || len(got.Projects[0].Exclusions) != 1 {
		t.Fatalf("project round-trip mismatch: %+v", got.Projects)
	}
	if len(got.Projects[0].PathMappings) != 2 || got.Projects[0].PathMappings[1].RemotePath != "/etc/site" {
		t.Fatalf("path mappings round-trip mismatch: %+v", got.Projects[0].PathMappings)
	}
}

func TestProjectPathMappingValidation(t *testing.T) {
	legacy := Project{RemoteRoot: "/srv"}
	if err := legacy.ValidatePathMappings(); err != nil {
		t.Fatalf("legacy remote_root should remain valid: %v", err)
	}
	if got := legacy.EffectivePathMappings(); len(got) != 1 || got[0].LocalPath != "" || got[0].RemotePath != "/srv" {
		t.Fatalf("legacy mapping = %+v", got)
	}

	bad := Project{PathMappings: []PathMapping{{LocalPath: "../outside", RemotePath: "/srv"}}}
	if err := bad.ValidatePathMappings(); err == nil {
		t.Fatal("expected escaping local path to fail validation")
	}
	overlap := Project{PathMappings: []PathMapping{
		{LocalPath: "public", RemotePath: "/web"},
		{LocalPath: "public/assets", RemotePath: "/assets"},
	}}
	if err := overlap.ValidatePathMappings(); err == nil {
		t.Fatal("expected overlapping mappings to fail validation")
	}
}

func TestEditorFieldParse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("editor: \"hx\"\naccounts: []\nprojects: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Editor != "hx" {
		t.Fatalf("editor = %q, want hx", cfg.Editor)
	}
	// Missing key defaults to empty (editor keys disabled).
	path2 := filepath.Join(dir, "noeditor.yaml")
	if err := os.WriteFile(path2, []byte("accounts: []\nprojects: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(path2)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Editor != "" {
		t.Fatalf("editor should default to empty, got %q", cfg2.Editor)
	}
}

func TestLoadOrEmpty(t *testing.T) {
	cfg, err := LoadOrEmpty(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Accounts) != 0 || len(cfg.Projects) != 0 {
		t.Fatalf("expected empty config, got %+v", cfg)
	}
}

func TestSaveEmptyConfigCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := (&Config{}).Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty yaml output")
	}
}

func TestSecretsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.yaml")
	s := &Secrets{Accounts: map[string]AccountSecret{
		"prod": {Password: "hunter2"},
		"web":  {Passphrase: "s3cret"},
	}}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 permissions, got %v", info.Mode().Perm())
	}
	got, err := LoadSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Accounts["prod"].Password != "hunter2" || got.Accounts["web"].Passphrase != "s3cret" {
		t.Fatalf("round-trip mismatch: %+v", got.Accounts)
	}
}

func TestLoadSecretsMissing(t *testing.T) {
	s, err := LoadSecrets(filepath.Join(t.TempDir(), "secrets.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Accounts) != 0 {
		t.Fatalf("expected empty secrets, got %+v", s.Accounts)
	}
}

func TestSecretsPath(t *testing.T) {
	want := filepath.Join(string(filepath.Separator), "etc", "godeploy", "secrets.yaml")
	if got := SecretsPath("/etc/godeploy/config.yaml"); got != want {
		t.Fatalf("SecretsPath = %q, want %q", got, want)
	}
}

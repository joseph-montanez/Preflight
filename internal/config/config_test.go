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

func TestValidateTasks(t *testing.T) {
	sftp := Account{ID: "sftp1", Protocol: "sftp", Host: "h"}
	ftp := Account{ID: "ftp1", Protocol: "ftp", Host: "h"}
	good := Task{ID: "deploy", Name: "Deploy", Steps: []TaskStep{
		{AccountID: "sftp1", Workdir: "apiv2", Run: "make build"},
		{AccountID: "sftp1", Run: "supervisorctl restart apiv2"},
	}}
	tests := []struct {
		name    string
		tasks   []Task
		wantErr string
	}{
		{name: "no tasks", tasks: nil, wantErr: ""},
		{name: "valid multi-step", tasks: []Task{good}, wantErr: ""},
		{name: "continue on error", tasks: []Task{{ID: "c", OnError: "continue",
			Steps: []TaskStep{{AccountID: "sftp1", Run: "x"}}}}, wantErr: ""},
		{name: "missing id", tasks: []Task{{Steps: []TaskStep{{AccountID: "sftp1", Run: "x"}}}},
			wantErr: "tasks[0].id is required"},
		{name: "duplicate id", tasks: []Task{good, {ID: "deploy",
			Steps: []TaskStep{{AccountID: "sftp1", Run: "x"}}}},
			wantErr: `duplicate task id "deploy"`},
		{name: "bad on error", tasks: []Task{{ID: "b", OnError: "retry",
			Steps: []TaskStep{{AccountID: "sftp1", Run: "x"}}}},
			wantErr: `task "b" on_error must be stop or continue`},
		{name: "no steps", tasks: []Task{{ID: "empty"}}, wantErr: `task "empty" has no steps`},
		{name: "empty run", tasks: []Task{{ID: "e",
			Steps: []TaskStep{{AccountID: "sftp1"}}}}, wantErr: `task "e" steps[0].run is required`},
		{name: "unknown account", tasks: []Task{{ID: "u",
			Steps: []TaskStep{{AccountID: "nope", Run: "x"}}}}, wantErr: `task "u" steps[0]: account "nope" not found`},
		{name: "ftp account rejected", tasks: []Task{{ID: "f",
			Steps: []TaskStep{{AccountID: "ftp1", Run: "x"}}}},
			wantErr: `task "f" steps[0]: account "ftp1" uses "ftp", only sftp accounts can run commands`},
		{name: "run plus remote rejected", tasks: []Task{{ID: "r",
			Steps: []TaskStep{{AccountID: "sftp1", Run: "x", Remote: "/srv/x"}}}},
			wantErr: `task "r" steps[0].remote requires upload`},
		{name: "run and upload exclusive", tasks: []Task{{ID: "b",
			Steps: []TaskStep{{AccountID: "sftp1", Run: "x", Upload: "f", Remote: "/srv/f"}}}},
			wantErr: `task "b" steps[0]: run and upload are mutually exclusive`},
		{name: "upload needs remote", tasks: []Task{{ID: "m",
			Steps: []TaskStep{{AccountID: "sftp1", Upload: "dist/a"}}}},
			wantErr: `task "m" steps[0].remote is required`},
		{name: "upload remote absolute", tasks: []Task{{ID: "a",
			Steps: []TaskStep{{AccountID: "sftp1", Upload: "dist/a", Remote: "srv/a"}}}},
			wantErr: `task "a" steps[0].remote must be an absolute path`},
		{name: "upload rejects workdir", tasks: []Task{{ID: "w",
			Steps: []TaskStep{{AccountID: "sftp1", Upload: "dist/a", Remote: "/srv/a", Workdir: "x"}}}},
			wantErr: `task "w" steps[0]: workdir/shell apply to run steps only`},
		{name: "upload rejects shell", tasks: []Task{{ID: "s",
			Steps: []TaskStep{{AccountID: "sftp1", Upload: "dist/a", Remote: "/srv/a", Shell: "bash -lc"}}}},
			wantErr: `task "s" steps[0]: workdir/shell apply to run steps only`},
		{name: "upload on sftp", tasks: []Task{{ID: "u",
			Steps: []TaskStep{{AccountID: "sftp1", Upload: "dist/a", Remote: "/srv/a"}}}}, wantErr: ""},
		{name: "upload on ftp", tasks: []Task{{ID: "uf",
			Steps: []TaskStep{{AccountID: "ftp1", Upload: "dist/a", Remote: "/srv/a"}}}}, wantErr: ""},
		{name: "mixed run upload run", tasks: []Task{{ID: "mix", Steps: []TaskStep{
			{AccountID: "sftp1", Run: "stop"},
			{AccountID: "sftp1", Upload: "dist/a", Remote: "/srv/a"},
			{AccountID: "sftp1", Run: "start"},
		}}}, wantErr: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Accounts: []Account{sftp, ftp}, Tasks: tt.tasks}
			err := cfg.ValidateTasks()
			if tt.wantErr == "" && err != nil {
				t.Fatalf("ValidateTasks() = %v, want nil", err)
			}
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ValidateTasks() = nil, want %q", tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("ValidateTasks() = %q, want %q", err.Error(), tt.wantErr)
				}
			}
		})
	}
}

func TestTaskStepKind(t *testing.T) {
	tests := []struct {
		name string
		step TaskStep
		want string
	}{
		{name: "run by default", step: TaskStep{AccountID: "a", Run: "x"}, want: TaskStepRun},
		{name: "upload when set", step: TaskStep{AccountID: "a", Upload: "f", Remote: "/srv/f"}, want: TaskStepUpload},
		{name: "blank upload is run", step: TaskStep{AccountID: "a", Upload: "  ", Run: "x"}, want: TaskStepRun},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.step.Kind(); got != tt.want {
				t.Fatalf("Kind() = %q, want %q", got, tt.want)
			}
			if got := tt.step.IsUpload(); got != (tt.want == TaskStepUpload) {
				t.Fatalf("IsUpload() = %v, want %v", got, tt.want == TaskStepUpload)
			}
		})
	}
}

func TestTaskLookupAndContinue(t *testing.T) {
	cfg := &Config{Tasks: []Task{
		{ID: "a", Steps: []TaskStep{{AccountID: "x", Run: "y"}}},
		{ID: "b", OnError: "Continue", Steps: []TaskStep{{AccountID: "x", Run: "y"}}},
	}}
	got, err := cfg.Task("a")
	if err != nil || got.ID != "a" || got.ContinueOnError() {
		t.Fatalf(`Task("a") = %+v, %v, want stop semantics`, got, err)
	}
	got, err = cfg.Task("b")
	if err != nil || !got.ContinueOnError() {
		t.Fatalf(`Task("b") should continue on error: %+v, %v`, got, err)
	}
	if _, err := cfg.Task("missing"); err == nil {
		t.Fatal(`Task("missing") should fail`)
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
	want := filepath.Join(string(filepath.Separator), "etc", "preflight", "secrets.yaml")
	if got := SecretsPath("/etc/preflight/config.yaml"); got != want {
		t.Fatalf("SecretsPath = %q, want %q", got, want)
	}
}

func TestIsMappingCovered(t *testing.T) {
	// Root mapping covers everything
	rootMappings := []PathMapping{{LocalPath: "", RemotePath: "/srv"}}
	if !IsMappingCovered("README.md", false, rootMappings) {
		t.Error("root mapping should cover README.md")
	}
	if !IsMappingCovered("src/app.go", false, rootMappings) {
		t.Error("root mapping should cover src/app.go")
	}
	if !IsMappingCovered("src", true, rootMappings) {
		t.Error("root mapping should cover src dir")
	}

	// Specific mappings
	mappings := []PathMapping{
		{LocalPath: "public", RemotePath: "/var/www/public"},
		{LocalPath: "client/dist", RemotePath: "/var/www/client"},
	}

	// Covered files
	if !IsMappingCovered("public/index.html", false, mappings) {
		t.Error("public/index.html should be covered")
	}
	if !IsMappingCovered("public/css/style.css", false, mappings) {
		t.Error("public/css/style.css should be covered")
	}
	if !IsMappingCovered("client/dist/bundle.js", false, mappings) {
		t.Error("client/dist/bundle.js should be covered")
	}

	// Covered directories
	if !IsMappingCovered("public", true, mappings) {
		t.Error("public dir should be covered")
	}
	if !IsMappingCovered("public/css", true, mappings) {
		t.Error("public/css dir should be covered")
	}
	if !IsMappingCovered("client", true, mappings) {
		t.Error("client dir (ancestor of client/dist) should be covered")
	}
	if !IsMappingCovered("client/dist", true, mappings) {
		t.Error("client/dist dir should be covered")
	}
	if !IsMappingCovered("", true, mappings) {
		t.Error("root dir should be covered as ancestor of mappings")
	}

	// Unmapped files
	if IsMappingCovered("README.md", false, mappings) {
		t.Error("README.md should NOT be covered")
	}
	if IsMappingCovered("Makefile", false, mappings) {
		t.Error("Makefile should NOT be covered")
	}
	if IsMappingCovered("client/package.json", false, mappings) {
		t.Error("client/package.json should NOT be covered (outside client/dist)")
	}
	if IsMappingCovered("tests/test.go", false, mappings) {
		t.Error("tests/test.go should NOT be covered")
	}

	// Unmapped directories
	if IsMappingCovered("tests", true, mappings) {
		t.Error("tests dir should NOT be covered")
	}
	if IsMappingCovered("client/src", true, mappings) {
		t.Error("client/src dir should NOT be covered")
	}
}

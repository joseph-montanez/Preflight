package main

import (
	"os"
	"path/filepath"
	"testing"

	"preflight/internal/config"
)

func TestShellArgsSftpKey(t *testing.T) {
	account := config.Account{
		ID: "prod", Protocol: "sftp", Host: "ssh.example.com", Port: 2222,
		Auth: config.Auth{Type: "key", Username: "deploy", KeyPath: "~/keys/id_ed25519"},
	}
	args, err := shellArgs(account)
	if err != nil {
		t.Fatalf("shellArgs: %v", err)
	}
	if len(args) != 5 || args[0] != "-p" || args[1] != "2222" || args[2] != "-i" {
		t.Fatalf("args = %v, want [-p 2222 -i <key> deploy@ssh.example.com]", args)
	}
	if !filepath.IsAbs(args[3]) || filepath.Base(args[3]) != "id_ed25519" || filepath.Dir(args[3]) == "keys" {
		t.Fatalf("key path should be absolute and expanded from ~: %q", args[3])
	}
	if args[4] != "deploy@ssh.example.com" {
		t.Fatalf("target = %q", args[4])
	}
}

func TestShellArgsDefaultPortNoKey(t *testing.T) {
	account := config.Account{
		ID: "prod", Protocol: "sftp", Host: "host.local", Port: 0,
		Auth: config.Auth{Type: "password", Username: "user"},
	}
	args, err := shellArgs(account)
	if err != nil {
		t.Fatalf("shellArgs: %v", err)
	}
	want := []string{"-p", "0", "user@host.local"}
	if len(args) != len(want) || args[0] != "-p" || args[2] != want[2] {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestShellArgsRejectsFtp(t *testing.T) {
	account := config.Account{ID: "legacy", Protocol: "ftps", Auth: config.Auth{Username: "u"}}
	if _, err := shellArgs(account); err == nil {
		t.Fatal("ftps account must be rejected for shell")
	}
}

func TestStoredSecretPriority(t *testing.T) {
	secrets := &config.Secrets{Accounts: map[string]config.AccountSecret{
		"pw":    {Password: "hunter2"},
		"keyed": {Password: "pw", Passphrase: "phrase"},
		"none":  {},
	}}
	cases := []struct {
		name    string
		account config.Account
		want    string
	}{
		{"password account uses stored password", config.Account{ID: "pw", Auth: config.Auth{Type: "password"}}, "hunter2"},
		{"key account without passphrase prompt uses password", config.Account{ID: "keyed", Auth: config.Auth{Type: "key"}}, "pw"},
		{"key account with passphrase prompt uses passphrase", config.Account{ID: "keyed", Auth: config.Auth{Type: "key", PassphrasePrompt: true}}, "phrase"},
		{"no stored secret", config.Account{ID: "none", Auth: config.Auth{Type: "password"}}, ""},
		{"unknown account", config.Account{ID: "ghost", Auth: config.Auth{Type: "password"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := storedSecret(tc.account, secrets); got != tc.want {
				t.Fatalf("storedSecret = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConfigureAskPassRoundTrip(t *testing.T) {
	secrets := &config.Secrets{Accounts: map[string]config.AccountSecret{
		"pw": {Password: "s3cret"},
	}}
	account := config.Account{ID: "pw", Auth: config.Auth{Type: "password"}}
	cleanup, err := configureAskPass(account, secrets)
	if err != nil {
		t.Fatalf("configureAskPass: %v", err)
	}
	defer cleanup()

	if got := os.Getenv("PREFLIGHT_ASKPASS_SECRET"); got != "s3cret" {
		t.Fatalf("secret env = %q, want s3cret", got)
	}
	if os.Getenv("SSH_ASKPASS") == "" || os.Getenv("SSH_ASKPASS_REQUIRE") != "force" {
		t.Fatalf("askpass env not configured: %q %q", os.Getenv("SSH_ASKPASS"), os.Getenv("SSH_ASKPASS_REQUIRE"))
	}
	// askpassMode prints the secret: swap stdout and run it.
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	askpassMode()
	_ = w.Close()
	os.Stdout = old
	buf := make([]byte, 64)
	n, _ := r.Read(buf)
	if got := string(buf[:n]); got != "s3cret\n" {
		t.Fatalf("askpass output = %q, want s3cret\\n", got)
	}

	cleanup()
	for _, key := range []string{"SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "PREFLIGHT_ASKPASS_SECRET"} {
		if os.Getenv(key) != "" {
			t.Fatalf("env %s should be unset after cleanup", key)
		}
	}
}

func TestConfigureAskPassNoSecret(t *testing.T) {
	account := config.Account{ID: "x", Auth: config.Auth{Type: "password"}}
	cleanup, err := configureAskPass(account, &config.Secrets{Accounts: map[string]config.AccountSecret{}})
	if err != nil {
		t.Fatalf("configureAskPass: %v", err)
	}
	cleanup()
	if os.Getenv("SSH_ASKPASS") != "" {
		t.Fatal("no secret must leave ssh to prompt interactively")
	}
}

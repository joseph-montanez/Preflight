package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"preflight/internal/config"
)

// openShell drops the user into an interactive ssh session for the
// account, reusing the real terminal after the TUI has exited (the ssh
// client handles its own pty, prompts, and signals — preflight only wires
// stdin/stdout/stderr through). Returns nil when the shell exited
// normally; any error is wrapped for the caller.
func openShell(account config.Account, secrets *config.Secrets) error {
	args, err := shellArgs(account)
	if err != nil {
		return err
	}
	sshPath, err := sshClientPath()
	if err != nil {
		return err
	}
	cleanup, err := configureAskPass(account, secrets)
	if err != nil {
		return err
	}
	defer cleanup()

	cmd := exec.Command(sshPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	fmt.Fprintf(os.Stderr, "Opening shell: %s@%s:%d (exit the shell to return to preflight)\n",
		account.Auth.Username, account.Host, account.Port)
	return cmd.Run()
}

// sshClientPath locates the ssh client. On Windows the canonical
// OpenSSH client in %SystemRoot%\System32\OpenSSH is preferred: PATH
// often resolves to the Git-Bash/WSL shim, which cannot run a Windows
// askpass program and has path-translation quirks. Falls back to PATH
// (and to the client's own interactive prompts) when it is absent.
func sshClientPath() (string, error) {
	if runtime.GOOS == "windows" {
		if systemRoot := os.Getenv("SystemRoot"); systemRoot != "" {
			candidate := filepath.Join(systemRoot, "System32", "OpenSSH", "ssh.exe")
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return "", fmt.Errorf("ssh client not found on PATH — install OpenSSH to use the shell")
	}
	return sshPath, nil
}

// shellArgs builds the ssh client arguments for the account. Password and
// passphrase prompts stay interactive unless a stored secret is
// available (see configureAskPass).
func shellArgs(account config.Account) ([]string, error) {
	switch strings.ToLower(account.Protocol) {
	case "sftp":
	case "":
		return nil, fmt.Errorf("account %q has no protocol", account.ID)
	default:
		return nil, fmt.Errorf("shell is only available for sftp accounts, %q uses %q", account.ID, account.Protocol)
	}
	args := []string{"-p", strconv.Itoa(account.Port)}
	if strings.EqualFold(account.Auth.Type, "key") && strings.TrimSpace(account.Auth.KeyPath) != "" {
		args = append(args, "-i", config.ExpandPath(account.Auth.KeyPath))
	}
	return append(args, account.Auth.Username+"@"+account.Host), nil
}

// configureAskPass makes ssh read the stored secret (password for
// password accounts, passphrase for key accounts) non-interactively by
// pointing SSH_ASKPASS at the preflight binary itself, which prints the
// secret in -askpass mode. The secret travels via an environment
// variable, never through a temp file. Without a stored secret it leaves
// ssh to prompt interactively in the terminal. SSH_ASKPASS_REQUIRE=force
// needs OpenSSH 8.4+; older clients ignore it and prompt interactively.
func configureAskPass(account config.Account, secrets *config.Secrets) (cleanup func(), err error) {
	secret := storedSecret(account, secrets)
	if secret == "" {
		return func() {}, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return func() {}, fmt.Errorf("resolve own path for askpass: %w", err)
	}
	if err := os.Setenv("SSH_ASKPASS", exe); err != nil {
		return func() {}, err
	}
	if err := os.Setenv("SSH_ASKPASS_REQUIRE", "force"); err != nil {
		return func() {}, err
	}
	if err := os.Setenv("PREFLIGHT_ASKPASS_SECRET", secret); err != nil {
		return func() {}, err
	}
	return func() {
		_ = os.Unsetenv("SSH_ASKPASS")
		_ = os.Unsetenv("SSH_ASKPASS_REQUIRE")
		_ = os.Unsetenv("PREFLIGHT_ASKPASS_SECRET")
	}, nil
}

// storedSecret picks the secret to feed ssh: the passphrase for key
// accounts that prompt for one, otherwise the account password.
func storedSecret(account config.Account, secrets *config.Secrets) string {
	if secrets == nil {
		return ""
	}
	stored, ok := secrets.Accounts[account.ID]
	if !ok {
		return ""
	}
	if strings.EqualFold(account.Auth.Type, "key") && account.Auth.PassphrasePrompt {
		return stored.Passphrase
	}
	return stored.Password
}

// askpassMode is the hidden -askpass entry point: ssh invokes this
// binary with the prompt as argv[1] and reads the secret from stdout.
func askpassMode() {
	fmt.Fprintln(os.Stdout, os.Getenv("PREFLIGHT_ASKPASS_SECRET"))
}

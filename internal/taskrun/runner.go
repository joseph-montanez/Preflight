// Package taskrun executes named multi-step remote command/file tasks
// (run configurations): ordered steps across accounts with live output,
// one connection per account, and stop-on-first-failure by default.
//
// Design (SOLID): the Run loop owns ordering, connection caching, and
// on_error handling only. Each step kind has its own StepExecutor
// (commandExecutor, uploadExecutor) behind a narrow interface, so new
// kinds extend the runner without modifying the loop (OCP) and either
// executor substitutes for the other in the loop (LSP). Run steps depend
// only on domain.Execer, upload steps only on domain.RemoteClient plus
// optional rename/chtimes capabilities (ISP). The runner depends on
// Lookup/Connector/OpenLocal abstractions, not concrete transports or
// the OS filesystem (DIP).
package taskrun

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"preflight/internal/config"
	"preflight/internal/domain"
)

// Connector dials an account and returns a connected client. Callers wire
// it to remote.NewClient plus their secret prompt; the runner calls it at
// most once per account per run.
type Connector func(account config.Account) (domain.RemoteClient, error)

// Lookup resolves an account id to its account.
type Lookup func(id string) (config.Account, error)

// LocalFile is the narrow file abstraction upload steps need (DIP): read,
// close, and stat for mtime preservation. *os.File satisfies it; tests
// substitute fakes.
type LocalFile interface {
	io.ReadCloser
	Stat() (os.FileInfo, error)
}

// OpenFunc opens a local file for an upload step. Defaults to os.Open.
type OpenFunc func(path string) (LocalFile, error)

// StepResult records one step's outcome. Skipped marks steps never started
// because an earlier step failed under stop-on-error.
type StepResult struct {
	Step    config.TaskStep
	Command string
	Err     error
	Skipped bool
}

// Failed reports whether the step ran and failed.
func (r StepResult) Failed() bool { return r.Err != nil && !r.Skipped }

// ComposeCommand builds the remote shell line for a step. A workdir is
// entered with cd first, so `cd apiv2` never has to be a step of its own;
// a failing cd keeps the command from running. A shell wraps the whole
// line as its single quoted argument, e.g. `bash -lc` runs a login shell
// that sources the profile (plain ssh exec skips it).
func ComposeCommand(workdir, run, shell string) string {
	base := run
	if strings.TrimSpace(workdir) != "" {
		base = "cd " + shellQuote(workdir) + " && " + run
	}
	if strings.TrimSpace(shell) == "" {
		return base
	}
	return strings.TrimSpace(shell) + " " + shellQuote(base)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// exitCode extracts the remote exit status from an Exec error, or -1 when
// the command never ran to an exit (dial failure, no shell, i/o error).
func exitCode(err error) int {
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitStatus()
	}
	return -1
}

// describeFailure renders a step failure for output and errors.
func describeFailure(err error) string {
	if code := exitCode(err); code >= 0 {
		return fmt.Sprintf("exit status %d", code)
	}
	return err.Error()
}

// StepExecutor runs one step kind against an already-dialed client.
// Display is the single-line header suffix shown as
// `▸ [account] <display>`; Command is the greppable command recorded in
// StepResult; Execute performs the step, streaming output to out.
type StepExecutor interface {
	Display(step config.TaskStep) string
	Command(step config.TaskStep) string
	Execute(step config.TaskStep, client domain.RemoteClient, out io.Writer) error
}

// commandExecutor runs run steps over domain.Execer (SFTP shell).
type commandExecutor struct{}

func (commandExecutor) Display(step config.TaskStep) string {
	return "$ " + commandExecutor{}.Command(step)
}

func (commandExecutor) Command(step config.TaskStep) string {
	return ComposeCommand(step.Workdir, step.Run, step.Shell)
}

func (commandExecutor) Execute(step config.TaskStep, client domain.RemoteClient, out io.Writer) error {
	execer, ok := client.(domain.Execer)
	if !ok {
		return fmt.Errorf("account %q cannot run commands (no shell)", step.AccountID)
	}
	return execer.Exec(ComposeCommand(step.Workdir, step.Run, step.Shell), out, out)
}

// uploadExecutor streams one local file to an absolute remote path.
type uploadExecutor struct {
	openLocal OpenFunc
}

func (u uploadExecutor) Display(step config.TaskStep) string {
	return u.Command(step)
}

func (uploadExecutor) Command(step config.TaskStep) string {
	return fmt.Sprintf("↑ %s → %s", strings.TrimSpace(step.Upload), strings.TrimSpace(step.Remote))
}

func (u uploadExecutor) Execute(step config.TaskStep, client domain.RemoteClient, out io.Writer) error {
	localPath := config.ExpandPath(strings.TrimSpace(step.Upload))
	remotePath := strings.TrimSpace(step.Remote)
	open := u.openLocal
	if open == nil {
		open = func(p string) (LocalFile, error) { return os.Open(p) }
	}
	written, err := uploadFile(client, open, localPath, remotePath)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%d bytes\n", written)
	return nil
}

// uploadFile streams localPath to remotePath: ensure the parent dir,
// write (atomically via tmp+rename when supported), restore mtime.
func uploadFile(client domain.RemoteClient, open OpenFunc, localPath, remotePath string) (int64, error) {
	if dir := path.Dir(remotePath); dir != "" && dir != "." {
		if err := client.MkdirAll(dir); err != nil {
			return 0, fmt.Errorf("create remote dir %s: %w", dir, err)
		}
	}
	if renamer, ok := client.(domain.RenameCapable); ok {
		tmp := remotePath + ".preflight-tmp"
		mtime, n, err := writeTarget(client, open, localPath, tmp)
		if err != nil {
			return n, err
		}
		if err := renamer.Rename(tmp, remotePath); err != nil {
			_ = client.Remove(tmp)
			// Some servers refuse rename-over-existing: retry as a direct
			// write by reopening the source, keeping the rename error if
			// the fallback fails too.
			if _, n2, err2 := writeTarget(client, open, localPath, remotePath); err2 == nil {
				_ = restoreRemoteTime(client, remotePath, mtime)
				return n2, nil
			}
			return n, fmt.Errorf("rename %s -> %s: %w", tmp, remotePath, err)
		}
		_ = restoreRemoteTime(client, remotePath, mtime)
		return n, nil
	}
	mtime, n, err := writeTarget(client, open, localPath, remotePath)
	if err != nil {
		return n, err
	}
	_ = restoreRemoteTime(client, remotePath, mtime)
	return n, nil
}

func writeTarget(client domain.RemoteClient, open OpenFunc, localPath, target string) (time.Time, int64, error) {
	src, err := open(localPath)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("open local %s: %w", localPath, err)
	}
	defer src.Close()
	var mtime time.Time
	if info, err := src.Stat(); err == nil && info != nil {
		mtime = info.ModTime()
	}
	dst, err := client.OpenWriter(target)
	if err != nil {
		return mtime, 0, fmt.Errorf("open remote %s: %w", target, err)
	}
	n, copyErr := io.Copy(dst, src)
	if closeErr := dst.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return mtime, n, fmt.Errorf("write %s: %w", target, copyErr)
	}
	return mtime, n, nil
}

func restoreRemoteTime(client domain.RemoteClient, remotePath string, mtime time.Time) error {
	times, ok := client.(domain.ChtimesCapable)
	if !ok || mtime.IsZero() {
		return nil
	}
	if err := times.Chtimes(remotePath, mtime, mtime); err != nil {
		return fmt.Errorf("set times %s: %w", remotePath, err)
	}
	return nil
}

// executorFor resolves the executor for a step's kind. New kinds add a
// case here without touching the Run loop (OCP).
func executorFor(step config.TaskStep, open OpenFunc) StepExecutor {
	if step.IsUpload() {
		return uploadExecutor{openLocal: open}
	}
	return commandExecutor{}
}

// Runner executes tasks with injectable dependencies (DIP). The zero
// OpenLocal defaults to os.Open.
type Runner struct {
	Lookup  Lookup
	Connect Connector
	Open    OpenFunc
}

// Run executes the task's steps in order, streaming headers and command
// output to out. Connections open lazily, at most one per account, and
// close when the run ends. It returns one result per step (later steps are
// marked skipped after a stop-on-error failure) and the first step error,
// or nil when every step succeeded.
func (r *Runner) Run(task config.Task, out io.Writer) ([]StepResult, error) {
	results := make([]StepResult, 0, len(task.Steps))
	conns := map[string]domain.RemoteClient{}
	defer func() {
		for _, client := range conns {
			_ = client.Close()
		}
	}()
	var firstErr error
	stopped := false
	for i := range task.Steps {
		step := task.Steps[i]
		if stopped {
			results = append(results, StepResult{Step: step, Skipped: true})
			continue
		}
		executor := executorFor(step, r.Open)
		err := func() error {
			account, err := r.Lookup(step.AccountID)
			if err != nil {
				return err
			}
			client, err := dial(conns, r.Connect, account)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "▸ [%s] %s\n", step.AccountID, executor.Display(step))
			return executor.Execute(step, client, out)
		}()
		results = append(results, StepResult{Step: step, Command: executor.Command(step), Err: err})
		if err == nil {
			fmt.Fprintf(out, "✓ step %d ok\n", i+1)
			continue
		}
		fmt.Fprintf(out, "✗ step %d failed: %s\n", i+1, describeFailure(err))
		if firstErr == nil {
			firstErr = err
		}
		if !task.ContinueOnError() {
			stopped = true
		}
	}
	if firstErr != nil {
		fmt.Fprintf(out, "task %q failed\n", task.ID)
		return results, firstErr
	}
	fmt.Fprintf(out, "task %q ok (%d/%d steps)\n", task.ID, len(results), len(task.Steps))
	return results, nil
}

// Run executes the task's steps in order with the default OS filesystem.
// It is a thin wrapper over Runner so existing callers stay unchanged.
func Run(task config.Task, lookup Lookup, connect Connector, out io.Writer) ([]StepResult, error) {
	return (&Runner{Lookup: lookup, Connect: connect}).Run(task, out)
}

// dial returns the cached connection for the account, opening it on first
// use.
func dial(conns map[string]domain.RemoteClient, connect Connector, account config.Account) (domain.RemoteClient, error) {
	if client, ok := conns[account.ID]; ok {
		return client, nil
	}
	client, err := connect(account)
	if err != nil {
		return nil, err
	}
	conns[account.ID] = client
	return client, nil
}

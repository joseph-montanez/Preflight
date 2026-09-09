// Package taskrun executes named multi-step remote command tasks (run
// configurations): ordered steps across SFTP accounts with live output,
// one connection per account, and stop-on-first-failure by default.
package taskrun

import (
	"errors"
	"fmt"
	"io"
	"strings"

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

// Run executes the task's steps in order, streaming headers and command
// output to out. Connections open lazily, at most one per account, and
// close when the run ends. It returns one result per step (later steps are
// marked skipped after a stop-on-error failure) and the first step error,
// or nil when every step succeeded.
func Run(task config.Task, lookup Lookup, connect Connector, out io.Writer) ([]StepResult, error) {
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
		command := ComposeCommand(step.Workdir, step.Run, step.Shell)
		err := func() error {
			account, err := lookup(step.AccountID)
			if err != nil {
				return err
			}
			client, err := dial(conns, connect, account)
			if err != nil {
				return err
			}
			execer, ok := client.(domain.Execer)
			if !ok {
				return fmt.Errorf("account %q cannot run commands (no shell)", account.ID)
			}
			fmt.Fprintf(out, "▸ [%s] $ %s\n", step.AccountID, command)
			return execer.Exec(command, out, out)
		}()
		results = append(results, StepResult{Step: step, Command: command, Err: err})
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

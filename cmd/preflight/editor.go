package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"preflight/internal/config"
	"preflight/internal/domain"

	"github.com/fsnotify/fsnotify"
)

// splitCommandLine splits a command line into an executable and its
// arguments, honoring single and double quotes. No shell is involved,
// so quoting rules are deliberately small: a quoted section keeps its
// contents verbatim (spaces included), an unterminated quote is an
// error. Config paths in quotes with spaces work; shell features like
// pipes or env expansion do not (point the setting at a script for
// those).
func splitCommandLine(line string) ([]string, error) {
	var parts []string
	var cur strings.Builder
	var quote byte
	started := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '"' || c == '\'':
			quote = c
			started = true
		case c == ' ' || c == '\t':
			if started {
				parts = append(parts, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in editor command %q", line)
	}
	if started {
		parts = append(parts, cur.String())
	}
	return parts, nil
}

// editorCommand resolves the configured editor command line into argv,
// expanding ~ on the executable token. Empty config yields a friendly
// error (the browsers normally catch this earlier and show a hint).
func editorCommand(editorCmd string) ([]string, error) {
	parts, err := splitCommandLine(editorCmd)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("no editor configured — add 'editor' to config.yaml")
	}
	parts[0] = config.ExpandPath(parts[0])
	return parts, nil
}

// openEditor runs the configured terminal editor on target (an absolute
// file or folder path) with the terminal handed over, mirroring the ssh
// shell flow: the TUI has already exited, the editor owns stdin/stdout,
// and returning from here returns to preflight. cwd is where the editor
// starts (project root for local edits, the temp dir for remote edits).
func openEditor(editorCmd, target, cwd string) error {
	argv, err := editorCommand(editorCmd)
	if err != nil {
		return err
	}
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("editor %q not found on PATH — check the 'editor' setting in config.yaml", argv[0])
	}
	args := append(append([]string{}, argv[1:]...), target)
	cmd := exec.Command(bin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = cwd
	fmt.Fprintf(os.Stderr, "Opening editor: %s %s (exit the editor to return to preflight)\n", bin, target)
	return cmd.Run()
}

// saveTick is the debounce between a detected save and the upload.
// Package-level so tests can shorten or disable it.
var saveTick = 400 * time.Millisecond

// openRemoteEdit downloads remotePath to a temp file, opens the
// configured editor on it, uploads on every save while the editor runs,
// and does a final hash-checked upload when it exits. The temp file is
// removed afterwards — unless the editor failed and the file changed,
// in which case it is kept and the remote is left at the last uploaded
// version. The watcher uses its own connection (newWatchClient), so it
// never shares state with the browser's connection.
func openRemoteEdit(client domain.RemoteClient, newWatchClient func() (domain.RemoteClient, error), editorCmd, remotePath string) error {
	base := filepath.Base(strings.ReplaceAll(remotePath, "/", string(os.PathSeparator)))
	tmp, err := os.CreateTemp("", "preflight-edit-*"+filepath.Ext(base))
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tempPath := tmp.Name()
	keepTemp := false // set when a failed editor leaves changes to recover
	defer func() {
		if !keepTemp {
			_ = os.Remove(tempPath)
		}
	}()
	r, err := client.OpenReader(remotePath)
	if err != nil {
		return fmt.Errorf("download %s: %w", remotePath, err)
	}
	if _, err := tmp.ReadFrom(r); err != nil {
		r.Close()
		_ = tmp.Close()
		return fmt.Errorf("download %s: %w", remotePath, err)
	}
	if err := r.Close(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("download %s: %w", remotePath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("download %s: %w", remotePath, err)
	}

	// Dedicated connection for the save-watcher uploads.
	watchClient, err := newWatchClient()
	if err != nil {
		return err
	}
	defer watchClient.Close()

	session, err := newEditSession(watchClient, tempPath, remotePath)
	if err != nil {
		return err
	}
	// The remote already holds the downloaded content: only deviations
	// from it count as edits.
	session.setBaseline()

	err = openEditor(editorCmd, tempPath, filepath.Dir(tempPath))
	session.stop()
	if err != nil {
		if session.changed() {
			// Editor failed and the file is not what was uploaded: keep
			// the temp copy for recovery instead of overwriting remotely.
			keepTemp = true
			fmt.Fprintf(os.Stderr,
				"preflight: editor exited with %v and %s differs from the last upload;\n"+
					"keeping %s for recovery — %s was NOT updated with the last edit\n",
				err, remotePath, tempPath, remotePath)
		}
		return err
	}
	if err := session.uploadIfChanged(); err != nil {
		return fmt.Errorf("final upload of %s: %w", remotePath, err)
	}
	return nil
}

// editSession watches a temp file and uploads it to a remote path
// whenever it changes (debounced), using its own client connection.
type editSession struct {
	client     domain.RemoteClient
	tempPath   string
	remotePath string
	done       chan struct{}
	mu         sync.Mutex // guards lastHash
	lastHash   string
}

// newEditSession starts a filesystem watcher on the temp file's
// directory (directory watches survive editors that save atomically by
// renaming a new file over the old one) and the debounced upload loop.
func newEditSession(client domain.RemoteClient, tempPath, remotePath string) (*editSession, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("watch %s: %w", tempPath, err)
	}
	if err := watcher.Add(filepath.Dir(tempPath)); err != nil {
		watcher.Close()
		return nil, fmt.Errorf("watch %s: %w", filepath.Dir(tempPath), err)
	}
	s := &editSession{client: client, tempPath: tempPath, remotePath: remotePath, done: make(chan struct{})}
	base := filepath.Base(tempPath)
	go func() {
		defer watcher.Close()
		ticker := time.NewTicker(saveTick)
		defer ticker.Stop()
		dirty := false
		for {
			select {
			case ev, ok := <-watcher.Events:
				if !ok {
					return
				}
				if filepath.Base(ev.Name) != base {
					continue
				}
				dirty = true
			case <-ticker.C:
				if dirty {
					dirty = false
					if err := s.uploadIfChanged(); err != nil {
						fmt.Fprintf(os.Stderr, "preflight: upload-on-save failed: %v\n", err)
					}
				}
			case <-s.done:
				return
			}
		}
	}()
	return s, nil
}

// stop ends the watch loop.
func (s *editSession) stop() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

// setBaseline records the temp file's current hash as the uploaded
// state, so an editor that changes nothing never triggers an upload.
func (s *editSession) setBaseline() {
	h, err := s.hash()
	if err != nil {
		return
	}
	s.mu.Lock()
	s.lastHash = h
	s.mu.Unlock()
}

// hash returns the SHA-256 of the temp file's current content.
func (s *editSession) hash() (string, error) {
	data, err := os.ReadFile(s.tempPath)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// changed reports whether the temp file differs from the last upload
// (or was never uploaded).
func (s *editSession) changed() bool {
	h, err := s.hash()
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return h != s.lastHash
}

// uploadIfChanged uploads the temp file to the remote path unless its
// content hash matches the last upload, and records the new hash.
func (s *editSession) uploadIfChanged() error {
	data, err := os.ReadFile(s.tempPath)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	s.mu.Lock()
	if hash == s.lastHash {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	w, err := s.client.OpenWriter(s.remotePath)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	s.mu.Lock()
	s.lastHash = hash
	s.mu.Unlock()
	return nil
}

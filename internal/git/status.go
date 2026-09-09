package git

import (
	"bytes"
	"errors"
	"os/exec"
	"sort"
	"strings"

	ignoreengine "preflight/internal/ignore"
)

var (
	// ErrGitNotFound indicates git is not installed or not present in PATH.
	ErrGitNotFound = errors.New("git executable not found in PATH")

	// ErrNotGitRepo indicates the directory is not inside a git repository.
	ErrNotGitRepo = errors.New("not a git repository")
)

// FileChange represents a single changed file detected by git status.
type FileChange struct {
	Path     string // project-relative path (slash-separated)
	OrigPath string // original path for renames/copies (empty if not renamed)
	Status   string // 2-letter porcelain status code, e.g. " M", "A ", "??", " D", "RM"
}

// IsGitRepo reports whether dir is inside a git working tree.
func IsGitRepo(dir string) bool {
	if _, err := exec.LookPath("git"); err != nil {
		return false
	}
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// Status returns all uncommitted changes in dir (modified, added, untracked,
// deleted, renamed). All returned paths are relative to dir.
func Status(dir string) ([]FileChange, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, ErrGitNotFound
	}

	// Determine subdirectory prefix within the git repository (e.g. for monorepos).
	prefixCmd := exec.Command("git", "-C", dir, "rev-parse", "--show-prefix")
	prefixOut, err := prefixCmd.Output()
	if err != nil {
		return nil, ErrNotGitRepo
	}
	prefix := strings.TrimSpace(string(prefixOut))

	// Run git status with porcelain v1, null-terminated, all untracked files,
	// restricted to the current directory (".").
	statusCmd := exec.Command("git", "-C", dir, "status", "--porcelain=v1", "-z", "-uall", ".")
	var stderr bytes.Buffer
	statusCmd.Stderr = &stderr
	out, err := statusCmd.Output()
	if err != nil {
		errStr := stderr.String()
		if strings.Contains(errStr, "not a git repository") {
			return nil, ErrNotGitRepo
		}
		return nil, errors.New(strings.TrimSpace(errStr))
	}

	return parsePorcelainZ(out, prefix), nil
}

// parsePorcelainZ parses git status --porcelain=v1 -z output.
func parsePorcelainZ(data []byte, prefix string) []FileChange {
	if len(data) == 0 {
		return nil
	}

	rawTokens := bytes.Split(data, []byte{0})
	var changes []FileChange

	for i := 0; i < len(rawTokens); i++ {
		token := string(rawTokens[i])
		if len(token) < 3 {
			continue
		}

		statusCode := token[:2]
		rawPath := token[3:]

		relPath := strings.TrimPrefix(rawPath, prefix)
		relPath = strings.TrimPrefix(relPath, "/")

		var origPath string
		// Renames (R) and copies (C) in porcelain -z place the original path in the next token.
		if (statusCode[0] == 'R' || statusCode[0] == 'C') && i+1 < len(rawTokens) {
			i++
			origRaw := string(rawTokens[i])
			origPath = strings.TrimPrefix(origRaw, prefix)
			origPath = strings.TrimPrefix(origPath, "/")
		}

		changes = append(changes, FileChange{
			Path:     relPath,
			OrigPath: origPath,
			Status:   statusCode,
		})
	}

	return changes
}

// ChangedPaths returns a sorted, deduplicated list of project-relative paths
// that have changes in git status. Any files matching the provided exclusions
// are filtered out. For renames, both the destination path and the source path
// are included so the sync planner can update the new file and clean up the old file.
func ChangedPaths(dir string, exclusions []string) ([]string, error) {
	changes, err := Status(dir)
	if err != nil {
		return nil, err
	}

	var ignores *ignoreengine.Engine
	if len(exclusions) > 0 {
		ignores = ignoreengine.NewExclusionsOnly(exclusions)
	}

	seen := make(map[string]bool)
	for _, c := range changes {
		addPath := func(p string) {
			p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
			p = strings.TrimPrefix(p, "./")
			p = strings.TrimPrefix(p, "/")
			if p == "" {
				return
			}
			if ignores != nil && ignores.RootIgnored(p, false) {
				return
			}
			seen[p] = true
		}

		addPath(c.Path)
		if c.OrigPath != "" {
			addPath(c.OrigPath)
		}
	}

	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}

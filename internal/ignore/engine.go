package ignore

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	gitignore "github.com/sabhiram/go-gitignore"
)

type Engine struct {
	projectRoot string
	patterns    []string
	rootIgnore  *gitignore.GitIgnore
}

// NewExclusionsOnly builds an engine that applies ONLY the configured
// exclusion patterns — no .gitignore is loaded. Used by the local upload
// browser, where gitignored files must be uploadable but project
// exclusions remain toggleable.
func NewExclusionsOnly(patterns []string) *Engine {
	return &Engine{patterns: normalizePatterns(patterns)}
}

func New(projectRoot string, patterns []string) (*Engine, error) {
	e := &Engine{projectRoot: projectRoot, patterns: normalizePatterns(patterns)}
	file, err := os.Open(filepath.Join(projectRoot, ".gitignore"))
	if err == nil {
		defer file.Close()
		var lines []string
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		e.rootIgnore = gitignore.CompileIgnoreLines(lines...)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return e, nil
}

func normalizePatterns(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(strings.ReplaceAll(pattern, "\\", "/"))
		if pattern == "" {
			continue
		}
		pattern = strings.TrimPrefix(pattern, "./")
		if strings.HasSuffix(pattern, "/") {
			pattern += "**"
		}
		out = append(out, pattern)
	}
	return out
}

func (e *Engine) Ignored(projectRelative, scopedRelative string, isDir bool) bool {
	projectRelative = strings.TrimPrefix(filepath.ToSlash(projectRelative), "./")
	scopedRelative = strings.TrimPrefix(filepath.ToSlash(scopedRelative), "./")
	if e.rootIgnore != nil {
		candidate := projectRelative
		if isDir && !strings.HasSuffix(candidate, "/") {
			candidate += "/"
		}
		if e.rootIgnore.MatchesPath(candidate) || e.rootIgnore.MatchesPath(projectRelative) {
			return true
		}
	}
	for _, pattern := range e.patterns {
		// Configured patterns match against the scope-relative path AND the
		// project-relative path, so a pattern like "kiosks/x.php" stays
		// effective no matter which scope is being synced.
		if e.matches(pattern, scopedRelative, isDir) || e.matches(pattern, projectRelative, isDir) {
			return true
		}
	}
	return false
}

func (e *Engine) matches(pattern, candidate string, isDir bool) bool {
	matched, _ := doublestar.Match(pattern, candidate)
	if matched {
		return true
	}
	if isDir {
		matched, _ = doublestar.Match(pattern, candidate+"/")
	}
	return matched
}

// Add appends patterns at runtime (e.g. the review TUI's ignore key), so
// already-running scans skip the newly ignored files too.
func (e *Engine) Add(patterns ...string) {
	e.patterns = append(e.patterns, normalizePatterns(patterns)...)
}

// RootIgnored reports whether a picked root path itself matches an
// exclusion pattern — the local upload browser's root-match rule. It
// matches against the project-relative path AND the bare name, so a
// pattern like "dist/" excludes a dist folder at any depth, mirroring
// how the sync scan treats the folder when its parent is the scope.
func (e *Engine) RootIgnored(projectRel string, isDir bool) bool {
	if e == nil {
		return false
	}
	projectRel = strings.TrimPrefix(filepath.ToSlash(projectRel), "./")
	return e.Ignored(projectRel, filepath.Base(projectRel), isDir)
}

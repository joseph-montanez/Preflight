package local

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"godeploy/internal/domain"
	ignoreengine "godeploy/internal/ignore"
)

func Scan(projectRoot, scope string, ignores *ignoreengine.Engine) (map[string]*domain.FileMeta, error) {
	return ScanProgress(projectRoot, scope, ignores, nil)
}

// ScanProgress is Scan with an optional per-file callback for progress
// reporting. A scope may be a folder (scanned recursively, files keyed
// by their path below the scope) or a single file (reported under the
// empty name, so the planner resolves the scope path itself).
func ScanProgress(projectRoot, scope string, ignores *ignoreengine.Engine, onFile func()) (map[string]*domain.FileMeta, error) {
	scope = filepath.Clean(scope)
	if scope == "." {
		scope = ""
	}
	scopeRoot := filepath.Join(projectRoot, scope)
	files := make(map[string]*domain.FileMeta)
	if info, err := os.Stat(scopeRoot); err == nil && !info.IsDir() {
		// Single-file scope: report it under the empty name so the planner
		// resolves the scope path itself. Root-match exclusion semantics
		// apply, mirroring the local upload browser's picked-root rule.
		projectRel, relErr := filepath.Rel(projectRoot, scopeRoot)
		if relErr != nil {
			return nil, relErr
		}
		if ignores != nil && ignores.RootIgnored(filepath.ToSlash(projectRel), false) {
			return files, nil
		}
		files[""] = &domain.FileMeta{
			Size:    info.Size(),
			ModTime: info.ModTime(),
			Mode:    info.Mode(),
			Exists:  true,
		}
		if onFile != nil {
			onFile()
		}
		return files, nil
	}
	err := filepath.WalkDir(scopeRoot, func(fullPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		scopedRel, err := filepath.Rel(scopeRoot, fullPath)
		if err != nil {
			return err
		}
		projectRel, err := filepath.Rel(projectRoot, fullPath)
		if err != nil {
			return err
		}
		if scopedRel == "." {
			return nil
		}
		if ignores != nil && ignores.Ignored(projectRel, scopedRel, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files[filepath.ToSlash(scopedRel)] = &domain.FileMeta{
			Size:    info.Size(),
			ModTime: info.ModTime(),
			Mode:    info.Mode(),
			Exists:  true,
		}
		if onFile != nil {
			onFile()
		}
		return nil
	})
	if err != nil && strings.Contains(err.Error(), "no such file or directory") {
		return files, err
	}
	return files, err
}

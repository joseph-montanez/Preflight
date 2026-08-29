package syncer

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"godeploy/internal/domain"
	ignoreengine "godeploy/internal/ignore"
)

func ScanRemote(client domain.RemoteClient, projectRoot, scope string, ignores *ignoreengine.Engine, onFile func()) (map[string]*domain.FileMeta, error) {
	return ScanRemoteMapped(client, projectRoot, scope, scope, ignores, onFile)
}

// ScanRemoteMapped scans remoteScope below remoteRoot while applying ignore
// rules using projectScope, the corresponding project-relative local path.
// A scope that is a single file is reported under the empty name, so the
// planner resolves the scope path itself. A missing remote base yields an
// empty map — the selection is simply new on the server (a new file to
// upload, or a folder whose contents are all uploads).
func ScanRemoteMapped(client domain.RemoteClient, remoteRoot, remoteScope, projectScope string, ignores *ignoreengine.Engine, onFile func()) (map[string]*domain.FileMeta, error) {
	remoteScope = strings.Trim(strings.ReplaceAll(remoteScope, "\\", "/"), "/")
	projectScope = strings.Trim(strings.ReplaceAll(projectScope, "\\", "/"), "/")
	base := path.Clean(path.Join(remoteRoot, remoteScope))
	files := make(map[string]*domain.FileMeta)
	meta, err := client.Stat(base)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("stat remote %s: %w", base, err)
	}
	if err == nil && !meta.Mode.IsDir() {
		if ignores != nil && ignores.RootIgnored(projectScope, false) {
			return files, nil
		}
		files[""] = meta
		if onFile != nil {
			onFile()
		}
		return files, nil
	}
	if err != nil {
		return files, nil
	}
	var walk func(current, scopedRel string) error
	walk = func(current, scopedRel string) error {
		entries, err := client.ReadDir(current)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() == "." || entry.Name() == ".." {
				continue
			}
			rel := path.Join(scopedRel, entry.Name())
			projectRel := path.Join(projectScope, rel)
			isDir := entry.IsDir()
			if ignores != nil && ignores.Ignored(projectRel, rel, isDir) {
				continue
			}
			full := path.Join(current, entry.Name())
			if isDir {
				if err := walk(full, rel); err != nil {
					return err
				}
				continue
			}
			if entry.Mode()&fs.ModeSymlink != 0 {
				continue
			}
			files[rel] = &domain.FileMeta{
				Size:    entry.Size(),
				ModTime: entry.ModTime(),
				Mode:    entry.Mode(),
				Exists:  true,
			}
			if onFile != nil {
				onFile()
			}
		}
		return nil
	}
	if err := walk(base, ""); err != nil {
		return nil, fmt.Errorf("scan remote %s: %w", base, err)
	}
	return files, nil
}

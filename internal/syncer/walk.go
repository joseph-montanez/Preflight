package syncer

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"

	"preflight/internal/domain"
)

// WalkRemote walks a remote directory recursively, calling onFile for
// every regular file with its absolute remote path and metadata. Unlike
// the sync scans it applies NO ignore rules — this is the escape hatch
// for downloading files that a sync would skip. Symlinks are skipped, as
// in the sync scans.
func WalkRemote(client domain.RemoteClient, remoteRoot string, onFile func(remotePath string, meta *domain.FileMeta)) error {
	remoteRoot = cleanRemoteRoot(remoteRoot)
	var walk func(current string) error
	walk = func(current string) error {
		entries, err := client.ReadDir(current)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() == "." || entry.Name() == ".." {
				continue
			}
			full := path.Join(current, entry.Name())
			if entry.IsDir() {
				if err := walk(full); err != nil {
					return err
				}
				continue
			}
			if entry.Mode()&fs.ModeSymlink != 0 {
				continue
			}
			onFile(full, &domain.FileMeta{
				Size:    entry.Size(),
				ModTime: entry.ModTime(),
				Mode:    entry.Mode(),
				Exists:  true,
			})
		}
		return nil
	}
	if err := walk(remoteRoot); err != nil {
		return fmt.Errorf("walk remote %s: %w", remoteRoot, err)
	}
	return nil
}

// WalkLocal walks a local directory recursively, calling onFile for
// every regular file with its absolute local path and metadata. It is
// the local mirror of WalkRemote: NO ignore rules are applied by the
// walk itself — the caller supplies an ignored callback (e.g. gating
// project exclusions) that receives the absolute path, the walk-
// relative path and the dir flag, and can prune directories and skip
// files. Symlinks are skipped, as in the remote walks, and empty
// directories are never reported (files only).
func WalkLocal(localRoot string, ignored func(absPath, walkRel string, isDir bool) bool, onFile func(localPath string, meta *domain.FileMeta)) error {
	localRoot = filepath.Clean(localRoot)
	err := filepath.WalkDir(localRoot, func(fullPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return walkErr
		}
		if fullPath == localRoot {
			return nil
		}
		walkRel, err := filepath.Rel(localRoot, fullPath)
		if err != nil {
			return err
		}
		if ignored != nil && ignored(fullPath, filepath.ToSlash(walkRel), entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		onFile(fullPath, &domain.FileMeta{
			Size:    info.Size(),
			ModTime: info.ModTime(),
			Mode:    info.Mode(),
			Exists:  true,
		})
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk local %s: %w", localRoot, err)
	}
	return nil
}

// cleanRemoteRoot normalizes a remote root for walking; the root "/"
// itself is walked directly.
func cleanRemoteRoot(root string) string {
	if root == "" {
		return "/"
	}
	return path.Clean(root)
}

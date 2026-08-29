package remotebrowser

import (
	"path/filepath"
	"strings"

	"godeploy/internal/config"
)

// ResolveLocalPath maps an absolute remote path to the local path it
// should be downloaded to, using the project's path mappings. When the
// remote path lies under a mapping's remote_path, the result is
// local_root + mapping.local_path + (suffix below the mapping root),
// where the suffix keeps the marked file name (or the dir tree for a
// marked folder); otherwise ok is false (the caller picks a destination).
//
// The longest matching remote prefix wins so overlapping mappings resolve
// to the most specific one.
func ResolveLocalPath(remotePath string, mappings []config.PathMapping, localRoot string) (localPath string, ok bool) {
	remotePath = cleanRemote(remotePath)
	bestLen := -1
	var best config.PathMapping
	for _, mapping := range mappings {
		remoteRoot := cleanRemote(mapping.RemotePath)
		if !under(remoteRoot, remotePath) {
			continue
		}
		if len(remoteRoot) > bestLen {
			bestLen = len(remoteRoot)
			best = mapping
		}
	}
	if bestLen < 0 {
		return "", false
	}
	suffix := strings.TrimPrefix(remotePath, cleanRemote(best.RemotePath))
	local := strings.Trim(strings.ReplaceAll(best.LocalPath, "\\", "/"), "/")
	if local == "." {
		local = ""
	}
	return filepath.Join(localRoot, filepath.FromSlash(filepath.Join(local, strings.TrimPrefix(suffix, "/")))), true
}

// cleanRemote normalizes a remote path: slash separators, no trailing
// slash (except the root itself).
func cleanRemote(value string) string {
	value = strings.ReplaceAll(value, "\\", "/")
	if value == "" || value == "/" {
		return "/"
	}
	return strings.TrimSuffix(value, "/")
}

// under reports whether child == parent or child lies below parent.
func under(parent, child string) bool {
	if parent == "" || parent == "/" {
		return true
	}
	return child == parent || strings.HasPrefix(child, parent+"/")
}

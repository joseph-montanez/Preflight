package localbrowser

import (
	"path"
	"path/filepath"
	"strings"

	"preflight/internal/config"
)

// ResolveRemotePath maps an absolute local path to the remote path it
// should be uploaded to, using the project's path mappings. When the
// local path lies under a mapping's local_path, the result is
// mapping.remote_path + (suffix below the mapping's local base), where
// the suffix keeps the marked file name (or the dir tree for a marked
// folder); otherwise ok is false (the caller asks for a destination).
//
// The longest matching local prefix wins so overlapping mappings resolve
// to the most specific one. A mapping with an empty local_path covers
// the project root itself.
func ResolveRemotePath(localPath string, mappings []config.PathMapping, localRoot string) (remotePath string, ok bool) {
	localPath = cleanLocal(localPath)
	root := cleanLocal(localRoot)
	bestLen := -1
	var best config.PathMapping
	for _, mapping := range mappings {
		localPathPart := strings.Trim(strings.ReplaceAll(mapping.LocalPath, "\\", "/"), "/")
		base := root
		if localPathPart != "" && localPathPart != "." {
			base = cleanLocal(path.Join(root, localPathPart))
		}
		if !under(base, localPath) {
			continue
		}
		if len(base) > bestLen {
			bestLen = len(base)
			best = mapping
		}
	}
	if bestLen < 0 {
		return "", false
	}
	suffix := strings.TrimPrefix(localPath, bestBase(root, best))
	return path.Join(strings.TrimSpace(best.RemotePath), strings.TrimPrefix(suffix, "/")), true
}

// bestBase re-derives the on-disk base of a mapping (same normalization
// as the matching loop) so the suffix keeps exactly the path below it.
func bestBase(root string, mapping config.PathMapping) string {
	localPathPart := strings.Trim(strings.ReplaceAll(mapping.LocalPath, "\\", "/"), "/")
	if localPathPart == "" || localPathPart == "." {
		return root
	}
	return cleanLocal(path.Join(root, localPathPart))
}

// cleanLocal normalizes a local path: forward slashes, no trailing slash
// (except the root itself).
func cleanLocal(value string) string {
	value = filepath.ToSlash(filepath.Clean(value))
	if value == "/" {
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

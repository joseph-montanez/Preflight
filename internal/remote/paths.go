package remote

import "strings"

// SplitRemote splits a remote path into a base directory and the last
// (incomplete) segment, matching the semantics needed for live directory
// completion: a trailing slash means the last segment is complete.
func SplitRemote(value string) (base, last string) {
	if value == "" || value == "/" {
		return "/", ""
	}
	if strings.HasSuffix(value, "/") {
		return strings.TrimSuffix(value, "/"), ""
	}
	idx := strings.LastIndex(value, "/")
	if idx < 0 {
		return "", value
	}
	return value[:idx], value[idx+1:]
}

// JoinRemote joins a remote base directory and an entry name, keeping
// the leading slash when the base is the filesystem root.
func JoinRemote(base, name string) string {
	switch {
	case base == "":
		return name
	case base == "/":
		return "/" + name
	case strings.HasSuffix(base, "/"):
		return base + name
	default:
		return base + "/" + name
	}
}

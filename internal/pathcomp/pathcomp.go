// Package pathcomp implements bash-style local path completion shared by
// the config forms and the folder browser.
package pathcomp

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SplitLocal splits a local path into a base directory and the last
// (incomplete) segment, expanding "~" first.
func SplitLocal(value string) (base, last string) {
	if value == "" {
		return ".", ""
	}
	value = ExpandTilde(value)
	cleaned := filepath.Clean(value)
	if strings.HasSuffix(value, string(os.PathSeparator)) || strings.HasSuffix(value, "/") {
		return cleaned, ""
	}
	return filepath.Dir(cleaned), filepath.Base(cleaned)
}

func ExpandTilde(value string) string {
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			if value == "~" {
				return home
			}
			return filepath.Join(home, value[2:])
		}
	}
	return value
}

func joinLocal(base, name string) string {
	if base == "." || base == "" {
		return name
	}
	if !strings.HasSuffix(base, string(os.PathSeparator)) {
		base += string(os.PathSeparator)
	}
	return base + name
}

// LocalPath returns full candidate values (base joined with the matching
// entry name). dirsOnly restricts results to directories.
func LocalPath(value string, dirsOnly bool) ([]string, error) {
	base, last := SplitLocal(value)
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if dirsOnly && !entry.IsDir() {
			continue
		}
		if !strings.HasPrefix(entry.Name(), last) {
			continue
		}
		name := entry.Name()
		if entry.IsDir() {
			name += string(os.PathSeparator)
		}
		names = append(names, joinLocal(base, name))
	}
	sortDirsFirst(names)
	return names, nil
}

func sortDirsFirst(names []string) {
	sort.SliceStable(names, func(i, j int) bool {
		di := strings.HasSuffix(names[i], string(os.PathSeparator))
		dj := strings.HasSuffix(names[j], string(os.PathSeparator))
		if di != dj {
			return di
		}
		return names[i] < names[j]
	})
}

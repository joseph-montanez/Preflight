package manage

import (
	"os"
	"sort"
	"strings"
)

// completeEnvName returns environment variable names matching the prefix.
func completeEnvName(value string) ([]string, error) {
	var names []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, value) && name != value {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

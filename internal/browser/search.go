package browser

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"preflight/internal/git"
)

type searchFilterKind uint8

const (
	searchByPath searchFilterKind = iota
	searchByType
	searchByExtension
	searchByContents
	searchByGit
)

type searchFilter struct {
	kind  searchFilterKind
	value string
}

// parseSearchQuery keeps the user's pipeline order intact. The first plain
// stage is the implicit %s filter; accepting plain search stages later too is
// intentional, while %s makes that intent unambiguous after another filter.
func parseSearchQuery(query string) ([]searchFilter, error) {
	stages := strings.Split(query, "|")
	filters := make([]searchFilter, 0, len(stages))
	for _, raw := range stages {
		stage := strings.TrimSpace(raw)
		if stage == "" {
			continue
		}
		if !strings.HasPrefix(stage, "%") {
			filters = append(filters, searchFilter{kind: searchByPath, value: stage})
			continue
		}

		body := strings.TrimSpace(strings.TrimPrefix(stage, "%"))
		cut := strings.IndexAny(body, ": \t")
		name, value := body, ""
		if cut >= 0 {
			name = body[:cut]
			value = strings.TrimSpace(strings.TrimLeft(body[cut:], ": \t"))
		}
		if name == "" {
			return nil, fmt.Errorf("expected a filter name after %%")
		}

		switch strings.ToLower(name) {
		case "s", "search":
			if value != "" {
				filters = append(filters, searchFilter{kind: searchByPath, value: value})
			}
		case "type":
			switch strings.ToLower(value) {
			case "":
				continue
			case "folder", "dir", "directory":
				value = "folder"
			case "file":
				value = "file"
			default:
				return nil, fmt.Errorf("%%type must be file or folder")
			}
			filters = append(filters, searchFilter{kind: searchByType, value: value})
		case "ext", "extension":
			if value != "" {
				// Accept the Markdown-looking \*.php form as well as *.php.
				value = strings.ReplaceAll(value, `\*`, "*")
				filters = append(filters, searchFilter{kind: searchByExtension, value: value})
			}
		case "contents", "content":
			if value != "" {
				filters = append(filters, searchFilter{kind: searchByContents, value: value})
			}
		case "git", "status":
			filters = append(filters, searchFilter{kind: searchByGit, value: value})
		default:
			return nil, fmt.Errorf("unknown filter %%%s", name)
		}
	}
	return filters, nil
}

func hasContentsFilter(filters []searchFilter) bool {
	for _, filter := range filters {
		if filter.kind == searchByContents {
			return true
		}
	}
	return false
}

// applySearchFilters evaluates stages from left to right. This is important
// for content lookup: a preceding path or extension stage reduces the number
// of files opened, while putting %contents first deliberately scans all files.
func applySearchFilters(ctx context.Context, root string, dirs, files []string, filters []searchFilter) ([]searchResult, error) {
	if len(filters) == 0 {
		return nil, nil
	}
	results := make([]searchResult, 0, len(dirs)+len(files))
	for _, dir := range dirs {
		results = append(results, searchResult{path: dir, dir: true})
	}
	for _, file := range files {
		results = append(results, searchResult{path: file})
	}

	for _, filter := range filters {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch filter.kind {
		case searchByPath:
			type scored struct {
				result searchResult
				score  int
			}
			matches := make([]scored, 0, len(results))
			for _, result := range results {
				if score := scorePath(result.path, result.dir, filter.value); score >= 0 {
					matches = append(matches, scored{result: result, score: score})
				}
			}
			sort.SliceStable(matches, func(i, j int) bool {
				if matches[i].score != matches[j].score {
					return matches[i].score < matches[j].score
				}
				if matches[i].result.dir != matches[j].result.dir {
					return matches[i].result.dir
				}
				return strings.ToLower(matches[i].result.path) < strings.ToLower(matches[j].result.path)
			})
			results = results[:0]
			for _, match := range matches {
				results = append(results, match.result)
			}
		case searchByType:
			wantDir := filter.value == "folder"
			results = keepSearchResults(results, func(result searchResult) bool { return result.dir == wantDir })
		case searchByExtension:
			pattern := strings.ToLower(strings.TrimSpace(filter.value))
			switch {
			case strings.HasPrefix(pattern, "."):
				pattern = "*" + pattern
			case !strings.ContainsAny(pattern, "*?["):
				pattern = "*." + strings.TrimPrefix(pattern, ".")
			}
			var matchErr error
			results = keepSearchResults(results, func(result searchResult) bool {
				if result.dir || matchErr != nil {
					return false
				}
				matched, err := path.Match(pattern, strings.ToLower(path.Base(result.path)))
				if err != nil {
					matchErr = err
				}
				return matched
			})
			if matchErr != nil {
				return nil, fmt.Errorf("invalid extension pattern: %w", matchErr)
			}
		case searchByContents:
			needle := []byte(filter.value)
			matched := make([]searchResult, 0, len(results))
			for _, result := range results {
				if result.dir {
					continue
				}
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				filename := filepath.Join(root, filepath.FromSlash(result.path))
				ok, err := fileContains(ctx, filename, needle)
				if err != nil {
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					continue // unreadable files simply do not match
				}
				if ok {
					matched = append(matched, result)
				}
			}
			results = matched
		case searchByGit:
			gitPaths, err := git.ChangedPaths(root, nil)
			if err != nil {
				return nil, fmt.Errorf("git: %w", err)
			}
			gitMap := make(map[string]bool, len(gitPaths))
			for _, gp := range gitPaths {
				gitMap[gp] = true
			}
			needle := strings.ToLower(strings.TrimSpace(filter.value))
			results = keepSearchResults(results, func(result searchResult) bool {
				if needle != "" && !strings.Contains(strings.ToLower(result.path), needle) {
					return false
				}
				if gitMap[result.path] {
					return true
				}
				if result.dir {
					for gp := range gitMap {
						if strings.HasPrefix(gp, result.path+"/") {
							return true
						}
					}
				}
				return false
			})
		}
	}
	return results, nil
}

func keepSearchResults(results []searchResult, keep func(searchResult) bool) []searchResult {
	filtered := make([]searchResult, 0, len(results))
	for _, result := range results {
		if keep(result) {
			filtered = append(filtered, result)
		}
	}
	return filtered
}

// scorePath preserves the existing basename ranking and additionally accepts
// consecutive path-segment prefixes: k/p/a matches kiosks/public_html/actions.
// A trailing slash scopes the match to a folder and everything below it.
func scorePath(rel string, dir bool, query string) int {
	q := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(query, `\`, "/")))
	folderScope := strings.HasSuffix(q, "/")
	q = strings.Trim(q, "/")
	if q == "" {
		return -1
	}
	dl := strings.ToLower(filepath.ToSlash(rel))
	base := path.Base(dl)
	if folderScope {
		return scoreFolderScope(dl, dir, q)
	}

	if strings.Contains(q, "/") {
		switch {
		case dl == q:
			return 0
		case strings.HasPrefix(dl, q+"/"):
			return 1
		case abbreviatedPathMatch(dl, q):
			return 2
		case strings.Contains(dl, q):
			return 3
		default:
			return -1
		}
	}
	if !strings.Contains(dl, q) {
		return -1
	}
	switch {
	case base == q:
		return 0
	case strings.HasPrefix(base, q):
		return 1
	case strings.Contains("/"+dl+"/", "/"+q+"/"):
		return 2
	default:
		return 3
	}
}

func scoreFolderScope(candidate string, dir bool, query string) int {
	candidateParts := strings.Split(candidate, "/")
	queryParts := strings.Split(query, "/")
	for start := 0; start+len(queryParts) <= len(candidateParts); start++ {
		matched := true
		for i, part := range queryParts {
			if part == "" || !strings.HasPrefix(candidateParts[start+i], part) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		end := start + len(queryParts)
		if end == len(candidateParts) {
			if dir {
				return 0
			}
			continue
		}
		return 2
	}
	return -1
}

func abbreviatedPathMatch(candidate, query string) bool {
	candidateParts := strings.Split(candidate, "/")
	queryParts := strings.Split(query, "/")
	if len(queryParts) < 2 || len(queryParts) > len(candidateParts) {
		return false
	}
	for start := 0; start+len(queryParts) <= len(candidateParts); start++ {
		matched := true
		for i, part := range queryParts {
			if part == "" || !strings.HasPrefix(candidateParts[start+i], part) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// fileContains searches in bounded chunks and retains enough overlap to find
// text spanning a chunk boundary. It avoids loading large files into memory.
func fileContains(ctx context.Context, filename string, needle []byte) (bool, error) {
	if len(needle) == 0 {
		return true, nil
	}
	file, err := os.Open(filename)
	if err != nil {
		return false, err
	}
	defer file.Close()

	const chunkSize = 64 * 1024
	buffer := make([]byte, chunkSize)
	overlap := make([]byte, 0, len(needle)-1)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			chunk := make([]byte, 0, len(overlap)+n)
			chunk = append(chunk, overlap...)
			chunk = append(chunk, buffer[:n]...)
			if bytes.Contains(chunk, needle) {
				return true, nil
			}
			keep := min(len(needle)-1, len(chunk))
			overlap = append(overlap[:0], chunk[len(chunk)-keep:]...)
		}
		if readErr != nil {
			if readErr == io.EOF {
				return false, nil
			}
			return false, readErr
		}
	}
}

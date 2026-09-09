package syncer

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"preflight/internal/config"
	diffengine "preflight/internal/diff"
	"preflight/internal/domain"
	ignoreengine "preflight/internal/ignore"
	"preflight/internal/local"
)

type Planner struct {
	Project config.Project
	Client  domain.RemoteClient
	Diff    *diffengine.Engine
	Ignore  *ignoreengine.Engine
	Scopes  []string // sync scopes relative to the project root; empty = full project

	// CompareClients is a pool of connections used to compare files in
	// parallel; each worker owns exactly one client, so any transport
	// (SFTP, FTP) is used by a single goroutine at a time. Falls back to
	// Client when empty.
	CompareClients []domain.RemoteClient

	// OnProgress reports scan stages (total <= 0 = unknown total).
	OnProgress func(stage string, done, total int)
}

func (p *Planner) progress(stage string, done, total int) {
	if p.OnProgress != nil {
		p.OnProgress(stage, done, total)
	}
}

func (p *Planner) Build() ([]*domain.SyncFileItem, error) {
	if err := p.Project.ValidatePathMappings(); err != nil {
		return nil, err
	}
	batchesToScan, err := p.resolveBatches()
	if err != nil {
		return nil, err
	}
	type scopeBatch struct {
		displayScope string
		localScope   string
		remoteRoot   string
		remoteScope  string
		localFiles   map[string]*domain.FileMeta
		remoteFiles  map[string]*domain.FileMeta
	}
	batches := make([]scopeBatch, 0, len(batchesToScan))
	var localScanned, remoteScanned int
	for _, batch := range batchesToScan {
		localFiles, err := local.ScanProgress(p.Project.LocalRoot, batch.localScope, p.Ignore, func() {
			localScanned++
			p.progress("scanning local files", localScanned, 0)
		})
		if err != nil {
			return nil, err
		}
		remoteFiles, err := ScanRemoteMapped(p.Client, batch.remoteRoot, batch.remoteScope, batch.displayScope, p.Ignore, func() {
			remoteScanned++
			p.progress("scanning remote files", remoteScanned, 0)
		})
		if err != nil {
			return nil, err
		}
		batches = append(batches, scopeBatch{
			displayScope: batch.displayScope,
			localScope:   batch.localScope,
			remoteRoot:   batch.remoteRoot,
			remoteScope:  batch.remoteScope,
			localFiles:   localFiles,
			remoteFiles:  remoteFiles,
		})
	}

	// Collect every (scope, name) pair and the compare total up front so
	// progress can report a stable fraction while workers run in parallel.
	type job struct {
		scope      string
		name       string
		localPath  string
		remotePath string
		local      *domain.FileMeta
		remote     *domain.FileMeta
	}
	var jobs []job
	compareTotal := 0
	for _, b := range batches {
		paths := make(map[string]struct{}, len(b.localFiles)+len(b.remoteFiles))
		for name := range b.localFiles {
			paths[name] = struct{}{}
		}
		for name := range b.remoteFiles {
			paths[name] = struct{}{}
		}
		for name := range paths {
			local := b.localFiles[name]
			if local == nil {
				local = &domain.FileMeta{}
			}
			remote := b.remoteFiles[name]
			if remote == nil {
				remote = &domain.FileMeta{}
			}
			jobs = append(jobs, job{
				scope:      b.displayScope,
				name:       name,
				localPath:  filepath.Join(p.Project.LocalRoot, filepath.FromSlash(path.Join(b.localScope, name))),
				remotePath: path.Join(b.remoteRoot, b.remoteScope, name),
				local:      local,
				remote:     remote,
			})
		}
		compareTotal += len(paths)
	}

	// Compare across parallel connections: one worker per client, each
	// worker owning its connection exclusively for the whole phase.
	clients := p.CompareClients
	if len(clients) == 0 {
		clients = []domain.RemoteClient{p.Client}
	}
	var (
		compared atomic.Int32
		mu       sync.Mutex
		items    []*domain.SyncFileItem
		errCh    = make(chan error, 1)
	)
	jobCh := make(chan job)
	var wg sync.WaitGroup
	for _, client := range clients {
		wg.Add(1)
		go func(client domain.RemoteClient) {
			defer wg.Done()
			for j := range jobCh {
				item := &domain.SyncFileItem{RelativePath: j.name, Scope: j.scope, LocalPath: j.localPath, RemotePath: j.remotePath, LocalStat: j.local, RemoteStat: j.remote}
				var err error
				item.Action, item.DiffDetails, err = p.compare(client, j.localPath, j.remotePath, j.local, j.remote)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					continue
				}
				mu.Lock()
				items = append(items, item)
				mu.Unlock()
				p.progress("comparing files", int(compared.Add(1)), compareTotal)
			}
		}(client)
	}
	for _, j := range jobs {
		jobCh <- j
	}
	close(jobCh)
	wg.Wait()
	select {
	case err := <-errCh:
		return nil, err
	default:
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Scope != items[j].Scope {
			return items[i].Scope < items[j].Scope
		}
		return items[i].RelativePath < items[j].RelativePath
	})
	return items, nil
}

type resolvedBatch struct {
	displayScope string
	localScope   string
	remoteRoot   string
	remoteScope  string
}

// resolveBatches intersects the paths selected in the browser with the
// project's configured mappings. Selecting the project root includes every
// mapping; selecting a path inside a mapping preserves the suffix below
// that mapping (a file scope resolves to the file itself).
func (p *Planner) resolveBatches() ([]resolvedBatch, error) {
	scopes := p.Scopes
	if len(scopes) == 0 {
		scopes = []string{""}
	}
	mappings := p.Project.EffectivePathMappings()
	seen := map[string]bool{}
	var batches []resolvedBatch
	for _, rawScope := range scopes {
		scope := cleanRelative(rawScope)
		matched := false
		for _, mapping := range mappings {
			localBase := cleanRelative(mapping.LocalPath)
			batch := resolvedBatch{remoteRoot: mapping.RemotePath}
			switch {
			case containsScope(scope, localBase):
				// The selected folder contains this mapping: scan the whole mapping.
				batch.displayScope = localBase
				batch.localScope = localBase
			case containsScope(localBase, scope):
				// The selection is inside this mapping: retain its relative suffix remotely.
				batch.displayScope = scope
				batch.localScope = scope
				batch.remoteScope = strings.TrimPrefix(strings.TrimPrefix(scope, localBase), "/")
			default:
				continue
			}
			matched = true
			key := batch.localScope + "\x00" + batch.remoteRoot + "\x00" + batch.remoteScope
			if !seen[key] {
				seen[key] = true
				batches = append(batches, batch)
			}
		}
		if !matched {
			if len(scopes) > 1 {
				// In multi-scope selection, skip unmapped entries rather than
				// failing the entire sync operation.
				continue
			}
			label := scope
			if label == "" {
				label = "."
			}
			return nil, fmt.Errorf("selected path %q is not covered by a path_mapping", label)
		}
	}
	if len(batches) == 0 {
		return nil, fmt.Errorf("no selected paths are covered by a path_mapping")
	}
	return batches, nil
}

func cleanRelative(value string) string {
	value = strings.Trim(strings.ReplaceAll(value, "\\", "/"), "/")
	if value == "." {
		return ""
	}
	return value
}

func containsScope(parent, child string) bool {
	return parent == "" || parent == child || strings.HasPrefix(child, parent+"/")
}

func (p *Planner) compare(client domain.RemoteClient, localPath, remotePath string, localMeta, remoteMeta *domain.FileMeta) (domain.ActionType, *domain.DiffResult, error) {
	if localMeta.Exists && !remoteMeta.Exists {
		return domain.ActionUpload, &domain.DiffResult{Summary: "local only"}, nil
	}
	if !localMeta.Exists && remoteMeta.Exists {
		return domain.ActionDownload, &domain.DiffResult{Summary: "remote only"}, nil
	}
	if !localMeta.Exists && !remoteMeta.Exists {
		return domain.ActionSkip, &domain.DiffResult{Identical: true}, nil
	}
	mode := domain.DiffMode(strings.ToLower(p.Project.DefaultDiffMode))
	switch mode {
	case domain.DiffModeSize:
		if localMeta.Size == remoteMeta.Size {
			return domain.ActionSkip, &domain.DiffResult{Identical: true, Summary: "same size"}, nil
		}
		return direction(localMeta, remoteMeta), &domain.DiffResult{Summary: "size differs"}, nil
	case domain.DiffModeModTime:
		if timesClose(localMeta.ModTime, remoteMeta.ModTime) {
			return domain.ActionSkip, &domain.DiffResult{Identical: true, Summary: "same modification time"}, nil
		}
		return direction(localMeta, remoteMeta), &domain.DiffResult{Summary: "modification time differs"}, nil
	case domain.DiffModeBinary:
		return compareByHash(client, localPath, remotePath, localMeta, remoteMeta, "")
	case domain.DiffModeText:
		if reason := textDiffSkipReason(localPath, remotePath, localMeta, remoteMeta); reason != "" {
			return compareByHash(client, localPath, remotePath, localMeta, remoteMeta, reason)
		}
		localData, tooLarge, err := readLocalTextDiffInput(localPath)
		if err != nil {
			return domain.ActionSkip, nil, err
		}
		if tooLarge {
			return compareByHash(client, localPath, remotePath, localMeta, remoteMeta, textDiffSizeReason)
		}
		reader, err := client.OpenReader(remotePath)
		if err != nil {
			return domain.ActionSkip, nil, err
		}
		remoteData, tooLarge, readErr := readTextDiffInput(reader)
		closeErr := reader.Close()
		if readErr != nil {
			return domain.ActionSkip, nil, readErr
		}
		if closeErr != nil {
			return domain.ActionSkip, nil, closeErr
		}
		if tooLarge {
			return compareByHash(client, localPath, remotePath, localMeta, remoteMeta, textDiffSizeReason)
		}
		details := p.Diff.CompareText(localData, remoteData, localPath, remotePath, diffengine.TextOptions{
			IgnoreWhitespace: p.Project.TextDiffOptions.IgnoreWhitespace,
			IgnoreBlankLines: p.Project.TextDiffOptions.IgnoreBlankLines,
			StripComments:    p.Project.TextDiffOptions.StripComments,
		})
		if details.Identical {
			return domain.ActionSkip, details, nil
		}
		return direction(localMeta, remoteMeta), details, nil
	default:
		return domain.ActionSkip, &domain.DiffResult{Summary: "unknown diff mode"}, nil
	}
}

// LoadTextDiff re-reads both sides and generates an unbounded text diff. It is
// intended for the review UI's explicit "view anyway" action.
func (p *Planner) LoadTextDiff(item *domain.SyncFileItem) (*domain.DiffResult, error) {
	if item == nil || item.LocalPath == "" || item.RemotePath == "" {
		return nil, fmt.Errorf("cannot load diff without both file paths")
	}
	localData, err := os.ReadFile(item.LocalPath)
	if err != nil {
		return nil, err
	}
	reader, err := p.Client.OpenReader(item.RemotePath)
	if err != nil {
		return nil, err
	}
	remoteData, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return p.Diff.CompareTextUnbounded(localData, remoteData, item.LocalPath, item.RemotePath, diffengine.TextOptions{
		IgnoreWhitespace: p.Project.TextDiffOptions.IgnoreWhitespace,
		IgnoreBlankLines: p.Project.TextDiffOptions.IgnoreBlankLines,
		StripComments:    p.Project.TextDiffOptions.StripComments,
	}), nil
}

const textDiffSizeReason = "text diff skipped: file exceeds 1 MiB"

func textDiffSkipReason(localPath, remotePath string, localMeta, remoteMeta *domain.FileMeta) string {
	if localMeta.Size > domain.MaxTextDiffBytes || remoteMeta.Size > domain.MaxTextDiffBytes {
		return textDiffSizeReason
	}
	if isLogPath(localPath) || isLogPath(remotePath) {
		return "text diff skipped: log file"
	}
	return ""
}

func isLogPath(filePath string) bool {
	name := strings.ToLower(filepath.Base(filePath))
	return strings.HasSuffix(name, ".log") || strings.Contains(name, ".log.")
}

func readLocalTextDiffInput(filePath string) ([]byte, bool, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	return readTextDiffInput(file)
}

func readTextDiffInput(reader io.Reader) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(reader, domain.MaxTextDiffBytes+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > domain.MaxTextDiffBytes {
		return nil, true, nil
	}
	return data, false, nil
}

func compareByHash(client domain.RemoteClient, localPath, remotePath string, localMeta, remoteMeta *domain.FileMeta, reason string) (domain.ActionType, *domain.DiffResult, error) {
	localHash, err := hashLocal(localPath)
	if err != nil {
		return domain.ActionSkip, nil, err
	}
	remoteHash, err := client.FastHash(remotePath)
	if err != nil {
		reader, openErr := client.OpenReader(remotePath)
		if openErr != nil {
			return domain.ActionSkip, nil, openErr
		}
		remoteHash, err = diffengine.HashReader(reader)
		_ = reader.Close()
		if err != nil {
			return domain.ActionSkip, nil, err
		}
	}
	if localHash == remoteHash {
		return domain.ActionSkip, &domain.DiffResult{Identical: true, Summary: hashSummary("SHA-256 matches", reason), TooLarge: reason == textDiffSizeReason}, nil
	}
	return direction(localMeta, remoteMeta), &domain.DiffResult{Summary: hashSummary("SHA-256 differs", reason), TooLarge: reason == textDiffSizeReason}, nil
}

func hashSummary(result, reason string) string {
	if reason == "" {
		return result
	}
	return fmt.Sprintf("%s (%s)", result, reason)
}

func direction(localMeta, remoteMeta *domain.FileMeta) domain.ActionType {
	if remoteMeta.ModTime.After(localMeta.ModTime.Add(2 * time.Second)) {
		return domain.ActionDownload
	}
	return domain.ActionUpload
}

func timesClose(a, b time.Time) bool {
	d := a.Sub(b)
	if d < 0 {
		d = -d
	}
	return d <= 2*time.Second
}

func hashLocal(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return diffengine.HashReader(file)
}

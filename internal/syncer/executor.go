package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"godeploy/internal/domain"
)

type Event struct {
	Path   string
	Scope  string
	Action domain.ActionType
	Err    error
}

// Progress reports the cumulative bytes copied for one transfer.
type Progress struct {
	Path   string
	Scope  string
	Action domain.ActionType
	Bytes  int64
}

type Executor struct {
	LocalRoot  string
	RemoteRoot string
	Client     domain.RemoteClient
	Workers    int
	OnEvent    func(Event)
	OnProgress func(Progress)
}

func (e *Executor) Execute(ctx context.Context, items []*domain.SyncFileItem) error {
	for _, item := range items {
		switch item.Action {
		case domain.ActionDeleteRemote, domain.ActionDeleteLocal:
			if err := e.executeOne(ctx, item); err != nil {
				e.emit(item, err)
				return err
			}
			e.emit(item, nil)
		}
	}
	// Create every remote directory needed by uploads once, up front, so
	// transfers don't pay a per-file MkdirAll round trip.
	dirs := map[string]struct{}{}
	for _, item := range items {
		if item.Action == domain.ActionUpload {
			dirs[path.Dir(e.remotePath(item))] = struct{}{}
		}
	}
	for d := range dirs {
		if err := e.Client.MkdirAll(d); err != nil {
			return fmt.Errorf("create remote dir %s: %w", d, err)
		}
	}
	workers := e.Workers
	if workers <= 0 {
		workers = 4
	}
	jobs := make(chan *domain.SyncFileItem)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				if item.Action != domain.ActionUpload && item.Action != domain.ActionDownload {
					continue
				}
				if err := e.executeOne(ctx, item); err != nil {
					e.emit(item, err)
					select {
					case errCh <- err:
					default:
					}
					continue
				}
				e.emit(item, nil)
			}
		}()
	}
	for _, item := range items {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		case jobs <- item:
		}
	}
	close(jobs)
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

func (e *Executor) executeOne(ctx context.Context, item *domain.SyncFileItem) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	localPath := e.localPath(item)
	remotePath := e.remotePath(item)
	switch item.Action {
	case domain.ActionUpload:
		return e.upload(item, localPath, remotePath, item.LocalStat)
	case domain.ActionDownload:
		return e.download(item, remotePath, localPath, item.RemoteStat)
	case domain.ActionDeleteRemote:
		if err := e.Client.Remove(remotePath); err != nil {
			return fmt.Errorf("delete remote %s: %w", remotePath, err)
		}
		return nil
	case domain.ActionDeleteLocal:
		if err := os.Remove(localPath); err != nil {
			return fmt.Errorf("delete local %s: %w", localPath, err)
		}
		return nil
	default:
		return nil
	}
}

func (e *Executor) localPath(item *domain.SyncFileItem) string {
	if item.LocalPath != "" {
		return item.LocalPath
	}
	return filepath.Join(e.LocalRoot, filepath.FromSlash(path.Join(item.Scope, item.RelativePath)))
}

func (e *Executor) remotePath(item *domain.SyncFileItem) string {
	if item.RemotePath != "" {
		return item.RemotePath
	}
	return path.Join(e.RemoteRoot, item.Scope, item.RelativePath)
}

func (e *Executor) upload(item *domain.SyncFileItem, localPath, remotePath string, meta *domain.FileMeta) error {
	input, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open local %s: %w", localPath, err)
	}
	defer input.Close()

	write := func(target string) error {
		output, err := e.Client.OpenWriter(target)
		if err != nil {
			return fmt.Errorf("open remote %s: %w", target, err)
		}
		if _, err := copyWithProgress(output, input, func(bytes int64) {
			e.emitProgress(item, bytes)
		}); err != nil {
			_ = output.Close()
			return fmt.Errorf("write %s: %w", target, err)
		}
		if err := output.Close(); err != nil {
			return fmt.Errorf("close %s: %w", target, err)
		}
		return nil
	}

	if renamer, ok := e.Client.(domain.RenameCapable); ok {
		tmp := remotePath + ".godeploy-tmp"
		if err := write(tmp); err != nil {
			return err
		}
		if err := renamer.Rename(tmp, remotePath); err != nil {
			_ = e.Client.Remove(tmp)
			// Some servers refuse to rename over an existing file: retry as
			// a direct write, keeping the original error if that fails too.
			if _, seekErr := input.Seek(0, io.SeekStart); seekErr == nil {
				if err2 := write(remotePath); err2 == nil {
					return e.setTimes(remotePath, meta)
				}
			}
			return fmt.Errorf("rename %s -> %s: %w", tmp, remotePath, err)
		}
		return e.setTimes(remotePath, meta)
	}
	if err := write(remotePath); err != nil {
		return err
	}
	return e.setTimes(remotePath, meta)
}

func (e *Executor) setTimes(remotePath string, meta *domain.FileMeta) error {
	times, ok := e.Client.(domain.ChtimesCapable)
	if !ok || meta == nil || meta.ModTime.IsZero() {
		return nil
	}
	if err := times.Chtimes(remotePath, meta.ModTime, meta.ModTime); err != nil {
		return fmt.Errorf("set times %s: %w", remotePath, err)
	}
	return nil
}

func (e *Executor) download(item *domain.SyncFileItem, remotePath, localPath string, meta *domain.FileMeta) error {
	input, err := e.Client.OpenReader(remotePath)
	if err != nil {
		return fmt.Errorf("open remote %s: %w", remotePath, err)
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("create local dir %s: %w", filepath.Dir(localPath), err)
	}
	tmpPath := localPath + ".godeploy-tmp"
	output, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create local %s: %w", tmpPath, err)
	}
	// The plan's remote size defines a finite best-effort snapshot. Reading
	// until EOF can chase a growing log indefinitely or reach a server error
	// after the originally observed bytes have already been copied.
	var source io.Reader = input
	if meta != nil && meta.Size >= 0 {
		source = io.LimitReader(input, meta.Size)
	}
	_, copyErr := copyWithProgress(output, source, func(bytes int64) {
		e.emitProgress(item, bytes)
	})
	closeErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write %s: %w", tmpPath, copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close %s: %w", tmpPath, closeErr)
	}
	if meta != nil && !meta.ModTime.IsZero() {
		if err := os.Chtimes(tmpPath, meta.ModTime, meta.ModTime); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("set times %s: %w", tmpPath, err)
		}
	}
	if err := replaceDownloadedFile(tmpPath, localPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace local %s with %s: %w", localPath, tmpPath, err)
	}
	return nil
}

// replaceDownloadedFile prefers an atomic rename. On Windows, another process
// may allow writes to an open log but deny the delete-sharing needed by rename.
// In that case, overwrite the existing regular file in place from the complete
// temporary download.
func replaceDownloadedFile(tmpPath, localPath string) error {
	renameErr := os.Rename(tmpPath, localPath)
	if renameErr == nil {
		return nil
	}
	targetInfo, err := os.Lstat(localPath)
	if err != nil || !targetInfo.Mode().IsRegular() {
		return renameErr
	}
	if err := overwriteExistingFile(tmpPath, localPath); err != nil {
		return errors.Join(renameErr, fmt.Errorf("overwrite open destination: %w", err))
	}
	_ = os.Remove(tmpPath)
	return nil
}

func overwriteExistingFile(sourcePath, targetPath string) error {
	input, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open completed download: %w", err)
	}
	defer input.Close()
	sourceInfo, err := input.Stat()
	if err != nil {
		return fmt.Errorf("stat completed download: %w", err)
	}
	output, err := os.OpenFile(targetPath, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open destination for in-place update: %w", err)
	}
	written, copyErr := io.Copy(output, input)
	if copyErr == nil {
		copyErr = output.Truncate(written)
	}
	if copyErr == nil {
		copyErr = output.Sync()
	}
	closeErr := output.Close()
	if copyErr != nil {
		return fmt.Errorf("write destination in place: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close destination: %w", closeErr)
	}
	if err := os.Chtimes(targetPath, sourceInfo.ModTime(), sourceInfo.ModTime()); err != nil {
		return fmt.Errorf("restore destination time: %w", err)
	}
	return nil
}

func (e *Executor) emit(item *domain.SyncFileItem, err error) {
	if e.OnEvent != nil {
		e.OnEvent(Event{Path: item.RelativePath, Scope: item.Scope, Action: item.Action, Err: err})
	}
}

func (e *Executor) emitProgress(item *domain.SyncFileItem, bytes int64) {
	if e.OnProgress != nil {
		e.OnProgress(Progress{
			Path:   item.RelativePath,
			Scope:  item.Scope,
			Action: item.Action,
			Bytes:  bytes,
		})
	}
}

func copyWithProgress(dst io.Writer, src io.Reader, onProgress func(int64)) (int64, error) {
	reader := &progressReader{reader: src, onProgress: onProgress}
	return io.Copy(dst, reader)
}

type progressReader struct {
	reader      io.Reader
	transferred int64
	onProgress  func(int64)
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.transferred += int64(n)
		if r.onProgress != nil {
			r.onProgress(r.transferred)
		}
	}
	return n, err
}

func Summary(items []*domain.SyncFileItem) string {
	counts := map[domain.ActionType]int{}
	for _, item := range items {
		counts[item.Action]++
	}
	return fmt.Sprintf("upload=%d download=%d delete-remote=%d delete-local=%d skip=%d",
		counts[domain.ActionUpload], counts[domain.ActionDownload], counts[domain.ActionDeleteRemote], counts[domain.ActionDeleteLocal], counts[domain.ActionSkip])
}

func TempSuffix() string {
	return fmt.Sprintf(".godeploy-tmp-%d", time.Now().UnixNano())
}

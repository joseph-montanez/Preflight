package ftpclient

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/textproto"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"preflight/internal/config"
	"preflight/internal/domain"

	ftp "github.com/jlaffaye/ftp"
)

type SecretPrompt func(label string) (string, error)

type Client struct {
	cfg    config.Account
	prompt SecretPrompt
	conn   *ftp.ServerConn
	mu     sync.Mutex
}

func New(cfg config.Account, prompt SecretPrompt) *Client {
	return &Client{cfg: cfg, prompt: prompt}
}

func (c *Client) Connect() error {
	address := net.JoinHostPort(c.cfg.Host, fmt.Sprintf("%d", c.cfg.Port))
	opts := []ftp.DialOption{ftp.DialWithTimeout(15 * time.Second)}
	if strings.EqualFold(c.cfg.Protocol, "ftps") {
		tlsConfig := &tls.Config{ServerName: c.cfg.Host, MinVersion: tls.VersionTLS12}
		if strings.EqualFold(c.cfg.TLSMode, "implicit") {
			opts = append(opts, ftp.DialWithTLS(tlsConfig))
		} else {
			opts = append(opts, ftp.DialWithExplicitTLS(tlsConfig))
		}
	}
	conn, err := ftp.Dial(address, opts...)
	if err != nil {
		return err
	}
	password := ""
	if c.cfg.Auth.PasswordEnv != "" {
		password = os.Getenv(c.cfg.Auth.PasswordEnv)
	}
	if password == "" && c.prompt != nil {
		password, err = c.prompt("FTP password")
		if err != nil {
			_ = conn.Quit()
			return err
		}
	}
	if err := conn.Login(c.cfg.Auth.Username, password); err != nil {
		_ = conn.Quit()
		return err
	}
	c.conn = conn
	return nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	return c.conn.Quit()
}

func (c *Client) Stat(remotePath string) (*domain.FileMeta, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	dir := path.Dir(remotePath)
	base := path.Base(remotePath)
	entries, err := c.conn.List(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Name == base {
			info := entryInfo{entry: entry}
			return &domain.FileMeta{Size: info.Size(), ModTime: info.ModTime(), Mode: info.Mode(), Exists: true}, nil
		}
	}
	return nil, fs.ErrNotExist
}

func (c *Client) ReadDir(remotePath string) ([]fs.FileInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := c.conn.List(remotePath)
	if err != nil {
		return nil, err
	}
	out := make([]fs.FileInfo, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entryInfo{entry: entry})
	}
	return out, nil
}

func (c *Client) OpenReader(remotePath string) (io.ReadCloser, error) {
	c.mu.Lock()
	reader, err := c.conn.Retr(remotePath)
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	return &unlockReadCloser{ReadCloser: reader, unlock: c.mu.Unlock}, nil
}

func (c *Client) OpenWriter(remotePath string) (io.WriteCloser, error) {
	reader, writer := io.Pipe()
	result := &ftpWriteCloser{PipeWriter: writer, done: make(chan error, 1)}
	go func() {
		c.mu.Lock()
		err := c.conn.Stor(remotePath, reader)
		c.mu.Unlock()
		_ = reader.CloseWithError(err)
		result.done <- err
		close(result.done)
	}()
	return result, nil
}

func (c *Client) Remove(remotePath string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.Delete(remotePath)
}

func (c *Client) MkdirAll(remotePath string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	remotePath = path.Clean(remotePath)
	if remotePath == "." || remotePath == "/" {
		return nil
	}
	absolute := strings.HasPrefix(remotePath, "/")
	parts := strings.Split(strings.Trim(remotePath, "/"), "/")
	current := ""
	if absolute {
		current = "/"
	}
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = path.Join(current, part)
		if absolute && !strings.HasPrefix(current, "/") {
			current = "/" + current
		}
		if err := c.conn.MakeDir(current); err != nil && !isAlreadyExists(err) {
			if _, listErr := c.conn.List(current); listErr != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Client) Rename(oldPath, newPath string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.Rename(oldPath, newPath)
}

func (c *Client) Chtimes(remotePath string, _, mtime time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.conn.IsSetTimeSupported() {
		return nil
	}
	return c.conn.SetTime(remotePath, mtime)
}

func (c *Client) FastHash(string) (string, error) {
	return "", domain.ErrFastHashUnsupported
}

type unlockReadCloser struct {
	io.ReadCloser
	unlock func()
	once   sync.Once
}

func (r *unlockReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(r.unlock)
	return err
}

type ftpWriteCloser struct {
	*io.PipeWriter
	done chan error
	once sync.Once
	err  error
}

func (w *ftpWriteCloser) Close() error {
	w.once.Do(func() {
		closeErr := w.PipeWriter.Close()
		storErr := <-w.done
		if closeErr != nil {
			w.err = closeErr
		} else {
			w.err = storErr
		}
	})
	return w.err
}

func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	var protoErr *textproto.Error
	if errors.As(err, &protoErr) {
		return protoErr.Code == 550 || protoErr.Code == 521
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "exist")
}

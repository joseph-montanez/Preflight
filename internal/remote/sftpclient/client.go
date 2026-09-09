package sftpclient

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"preflight/internal/config"
	"preflight/internal/domain"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type SecretPrompt func(label string) (string, error)

type Client struct {
	cfg       config.Account
	prompt    SecretPrompt
	sshClient *ssh.Client
	sftp      *sftp.Client
	agentConn net.Conn
}

func New(cfg config.Account, prompt SecretPrompt) *Client {
	return &Client{cfg: cfg, prompt: prompt}
}

func (c *Client) Connect() error {
	auth, err := c.authMethods()
	if err != nil {
		return err
	}
	sshConfig := &ssh.ClientConfig{
		User:            c.cfg.Auth.Username,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}
	address := net.JoinHostPort(c.cfg.Host, fmt.Sprintf("%d", c.cfg.Port))
	c.sshClient, err = ssh.Dial("tcp", address, sshConfig)
	if err != nil {
		return err
	}
	c.sftp, err = sftp.NewClient(c.sshClient)
	if err != nil {
		_ = c.sshClient.Close()
		return err
	}
	return nil
}

func (c *Client) authMethods() ([]ssh.AuthMethod, error) {
	switch strings.ToLower(c.cfg.Auth.Type) {
	case "key":
		key, err := os.ReadFile(config.ExpandPath(c.cfg.Auth.KeyPath))
		if err != nil {
			return nil, err
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			var passphraseErr *ssh.PassphraseMissingError
			if errors.As(err, &passphraseErr) && c.cfg.Auth.PassphrasePrompt && c.prompt != nil {
				passphrase, promptErr := c.prompt("SSH key passphrase")
				if promptErr != nil {
					return nil, promptErr
				}
				signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(passphrase))
			}
		}
		if err != nil {
			return nil, err
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	case "agent":
		socket := os.Getenv("SSH_AUTH_SOCK")
		if socket == "" {
			return nil, errors.New("SSH_AUTH_SOCK is not set")
		}
		conn, err := net.Dial("unix", socket)
		if err != nil {
			return nil, err
		}
		c.agentConn = conn
		agentClient := agent.NewClient(conn)
		return []ssh.AuthMethod{ssh.PublicKeysCallback(agentClient.Signers)}, nil
	case "password":
		password := ""
		if c.cfg.Auth.PasswordEnv != "" {
			password = os.Getenv(c.cfg.Auth.PasswordEnv)
		}
		if password == "" && c.prompt != nil {
			var err error
			password, err = c.prompt("SSH password")
			if err != nil {
				return nil, err
			}
		}
		if password == "" {
			return nil, errors.New("password auth selected but no password is available")
		}
		return []ssh.AuthMethod{ssh.Password(password)}, nil
	default:
		return nil, fmt.Errorf("unsupported SFTP auth type %q", c.cfg.Auth.Type)
	}
}

func (c *Client) Close() error {
	var first error
	if c.sftp != nil {
		first = c.sftp.Close()
	}
	if c.sshClient != nil {
		if err := c.sshClient.Close(); first == nil {
			first = err
		}
	}
	if c.agentConn != nil {
		if err := c.agentConn.Close(); first == nil {
			first = err
		}
	}
	return first
}

func (c *Client) Stat(path string) (*domain.FileMeta, error) {
	info, err := c.sftp.Stat(path)
	if err != nil {
		return nil, err
	}
	return &domain.FileMeta{Size: info.Size(), ModTime: info.ModTime(), Mode: info.Mode(), Exists: true}, nil
}

func (c *Client) ReadDir(path string) ([]fs.FileInfo, error) {
	return c.sftp.ReadDir(path)
}

func (c *Client) OpenReader(path string) (io.ReadCloser, error) {
	return c.sftp.Open(path)
}

func (c *Client) OpenWriter(path string) (io.WriteCloser, error) {
	return c.sftp.Create(path)
}

func (c *Client) Remove(path string) error {
	return c.sftp.Remove(path)
}

func (c *Client) MkdirAll(path string) error {
	// Prefer the shell: `mkdir -p` is robust for deep trees, existing
	// directories, and servers whose SFTP subsystem reports generic
	// failures for recursive mkdir (e.g. chrooted homes).
	if c.sshClient != nil {
		session, err := c.sshClient.NewSession()
		if err == nil {
			quoted := shellQuote(path)
			output, runErr := session.CombinedOutput("mkdir -p -- " + quoted)
			_ = session.Close()
			if runErr == nil {
				return nil
			}
			// Fall through to the protocol-level fallback even when the
			// shell failed with output (e.g. servers that reject exec
			// channels entirely); keep the shell error for context only
			// if the fallback fails too.
			shellErr := fmt.Errorf("mkdir -p %s: %s", quoted, strings.TrimSpace(string(output)))
			if err2 := c.sftp.MkdirAll(path); err2 != nil {
				return errors.Join(shellErr, err2)
			}
			return nil
		}
	}
	// Fallback: the protocol-level recursive MkdirAll.
	return c.sftp.MkdirAll(path)
}

func (c *Client) Rename(oldPath, newPath string) error {
	return c.sftp.Rename(oldPath, newPath)
}

func (c *Client) Chtimes(path string, atime, mtime time.Time) error {
	return c.sftp.Chtimes(path, atime, mtime)
}

func (c *Client) FastHash(path string) (string, error) {
	if c.sshClient == nil {
		return "", errors.New("SFTP client is not connected")
	}
	session, err := c.sshClient.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	quoted := shellQuote(path)
	output, err := session.Output("sha256sum -- " + quoted)
	if err != nil {
		return "", err
	}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	if !scanner.Scan() {
		return "", errors.New("sha256sum returned no output")
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) == 0 || len(fields[0]) != 64 {
		return "", fmt.Errorf("unexpected sha256sum output: %q", scanner.Text())
	}
	return strings.ToLower(fields[0]), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// Exec runs a shell command on the remote host, streaming stdout and stderr
// as the command runs. A non-zero exit status returns the ssh exit error;
// callers report it with the status code.
func (c *Client) Exec(command string, stdout, stderr io.Writer) error {
	if c.sshClient == nil {
		return errors.New("SFTP client is not connected")
	}
	session, err := c.sshClient.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdout = stdout
	session.Stderr = stderr
	return session.Run(command)
}

func ExpandKeyPath(path string) string {
	if path == "" {
		return path
	}
	return filepath.Clean(config.ExpandPath(path))
}

package remote

import (
	"fmt"
	"strings"

	"preflight/internal/config"
	"preflight/internal/domain"
	"preflight/internal/remote/ftpclient"
	"preflight/internal/remote/sftpclient"
)

type SecretPrompt func(label string) (string, error)

func NewClient(account config.Account, prompt SecretPrompt) (domain.RemoteClient, error) {
	switch strings.ToLower(account.Protocol) {
	case "sftp":
		return sftpclient.New(account, sftpclient.SecretPrompt(prompt)), nil
	case "ftp", "ftps":
		return ftpclient.New(account, ftpclient.SecretPrompt(prompt)), nil
	default:
		return nil, fmt.Errorf("unsupported protocol %q", account.Protocol)
	}
}

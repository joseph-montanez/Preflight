package manage

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"godeploy/internal/config"
	"godeploy/internal/domain"
	"godeploy/internal/remote"
)

// remoteCompleter lazily opens one connection per account and keeps it
// alive while a project form is open, so remote_root Tab completion can
// list directories on the actual server.
type remoteCompleter struct {
	mu      sync.Mutex
	client  domain.RemoteClient
	account *config.Account
	secrets *config.Secrets
}

func (r *remoteCompleter) complete(acc *config.Account, value string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client == nil || r.account == nil || r.account.ID != acc.ID {
		if r.client != nil {
			_ = r.client.Close()
		}
		client, err := remote.NewClient(*acc, r.promptFor(acc.ID))
		if err != nil {
			return nil, err
		}
		if err := client.Connect(); err != nil {
			return nil, err
		}
		r.client, r.account = client, acc
	}
	return listRemoteDirs(r.client, value)
}

// promptFor resolves the account's stored secret (password or key
// passphrase from secrets.yaml); the project editor has no interactive
// prompt, so this is the only source besides password_env.
func (r *remoteCompleter) promptFor(accountID string) remote.SecretPrompt {
	return func(label string) (string, error) {
		if r.secrets != nil {
			if stored, ok := r.secrets.Accounts[accountID]; ok {
				if strings.Contains(label, "passphrase") && stored.Passphrase != "" {
					return stored.Passphrase, nil
				}
				if stored.Password != "" {
					return stored.Password, nil
				}
			}
		}
		return "", errors.New("no secret available in the project editor — set the password in the Accounts tab or set password_env")
	}
}

func (r *remoteCompleter) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client != nil {
		_ = r.client.Close()
		r.client = nil
		r.account = nil
	}
}

// listRemoteDirs returns full candidate values (base joined with the
// matching directory name, trailing slash included).
func listRemoteDirs(client domain.RemoteClient, value string) ([]string, error) {
	base, last := remote.SplitRemote(value)
	infos, err := client.ReadDir(base)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		if !info.IsDir() {
			continue
		}
		if !strings.HasPrefix(info.Name(), last) {
			continue
		}
		names = append(names, remote.JoinRemote(base, info.Name()+"/"))
	}
	sort.Strings(names)
	return names, nil
}

// testAccountConnection dials the account, using the given prompt to
// resolve passwords/passphrases (typed secret, stored secret, or error).
func testAccountConnection(acc config.Account, prompt remote.SecretPrompt) error {
	client, err := remote.NewClient(acc, prompt)
	if err != nil {
		return err
	}
	defer client.Close()
	return client.Connect()
}

func testAccount(acc config.Account, prompt remote.SecretPrompt) string {
	if err := testAccountConnection(acc, prompt); err != nil {
		return "connection failed: " + err.Error()
	}
	return fmt.Sprintf("OK — %s connected as %s@%s:%d", strings.ToUpper(acc.Protocol), acc.Auth.Username, acc.Host, acc.Port)
}

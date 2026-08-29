package manage

import (
	"fmt"
	"strconv"
	"strings"

	"godeploy/internal/config"
	"godeploy/internal/pathcomp"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	tagID       = "id"
	tagName     = "name"
	tagProtocol = "protocol"
	tagHost     = "host"
	tagPort     = "port"
	tagTLS      = "tls"
	tagAuth     = "auth"
	tagUser     = "username"
	tagKey      = "key_path"
	tagPassHint = "passphrase"
	tagPassEnv  = "password_env"
	tagSecret   = "secret"
)

func defaultPort(protocol string) int {
	switch strings.ToLower(protocol) {
	case "ftp", "ftps":
		return 21
	default:
		return 22
	}
}

func boolVal(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// secretFieldHint describes the stored state of an account's secret.
func secretFieldHint(secrets *config.Secrets, accountID string) string {
	if secrets != nil {
		if stored := secrets.Accounts[accountID]; stored.Password != "" || stored.Passphrase != "" {
			return "stored •••• — type to replace · Ctrl+X clears"
		}
	}
	return "saved to secrets.yaml — never written to config.yaml"
}

// openAccountForm opens the account editor; idx == -1 creates a new one.
func (m *Model) openAccountForm(idx int) {
	acc := config.Account{}
	if idx >= 0 && idx < len(m.cfg.Accounts) {
		acc = m.cfg.Accounts[idx]
	} else {
		acc = config.Account{Protocol: "sftp", Port: 22, Auth: config.Auth{Type: "key"}}
	}
	title := "New Account"
	if idx >= 0 {
		title = "Account: " + acc.ID
	}

	// When the protocol changes, follow the port if it still holds the
	// previous protocol's default.
	prevProtocol := acc.Protocol
	onProtocol := func(f *Form, i int) {
		cur := f.Fields[i].Value
		if cur == prevProtocol {
			return
		}
		if p := f.byTag(tagPort); p.Value == strconv.Itoa(defaultPort(prevProtocol)) {
			f.setValue(f.byTagIdx(tagPort), strconv.Itoa(defaultPort(cur)))
		}
		prevProtocol = cur
	}

	// The secret field stores a real password/passphrase in secrets.yaml;
	// its label and hint follow the auth type.
	secretLabel := "Password"
	switch acc.Auth.Type {
	case "key":
		secretLabel = "Key passphrase"
	case "agent":
		secretLabel = "Secret"
	}
	secretHint := secretFieldHint(m.secrets, acc.ID)
	if acc.Auth.Type == "agent" {
		secretHint = "unused for agent auth"
	}
	onAuth := func(f *Form, i int) {
		secret := f.byTag(tagSecret)
		switch f.Fields[i].Value {
		case "password":
			secret.Label = "Password"
			secret.Hint = secretFieldHint(m.secrets, f.byTag(tagID).Value)
		case "key":
			secret.Label = "Key passphrase"
			secret.Hint = secretFieldHint(m.secrets, f.byTag(tagID).Value)
		default:
			secret.Label = "Secret"
			secret.Hint = "unused for agent auth"
		}
	}

	fields := []Field{
		{Tag: tagID, Kind: KindText, Label: "ID", Value: acc.ID, Placeholder: "prod-web"},
		{Tag: tagName, Kind: KindText, Label: "Name", Value: acc.Name, Placeholder: "Production Web 01"},
		{Tag: tagProtocol, Kind: KindSelect, Label: "Protocol", Value: acc.Protocol, Options: []string{"sftp", "ftp", "ftps"}, OnChange: onProtocol},
		{Tag: tagHost, Kind: KindText, Label: "Host", Value: acc.Host, Placeholder: "ssh.example.com"},
		{Tag: tagPort, Kind: KindText, Label: "Port", Value: strconv.Itoa(acc.Port), Placeholder: strconv.Itoa(defaultPort(acc.Protocol))},
		{Tag: tagTLS, Kind: KindSelect, Label: "TLS mode", Value: acc.TLSMode, Options: []string{"", "explicit", "implicit"}, Display: func(v string) string {
			if v == "" {
				return "none"
			}
			return v
		}},
		{Tag: tagAuth, Kind: KindSelect, Label: "Auth type", Value: acc.Auth.Type, Options: []string{"key", "agent", "password"}, OnChange: onAuth},
		{Tag: tagUser, Kind: KindText, Label: "Username", Value: acc.Auth.Username, Placeholder: "deploy"},
		{Tag: tagKey, Kind: KindFile, Label: "Key path", Value: acc.Auth.KeyPath, Placeholder: "~/.ssh/id_ed25519", Hint: "Tab to complete", Complete: func(v string) ([]string, error) {
			return pathcomp.LocalPath(v, false)
		}},
		{Tag: tagPassHint, Kind: KindBool, Label: "Passphrase prompt", Value: boolVal(acc.Auth.PassphrasePrompt)},
		{Tag: tagPassEnv, Kind: KindEnv, Label: "Password env", Value: acc.Auth.PasswordEnv, Placeholder: "GODEPLOY_PASSWORD", Hint: "Tab to complete", Complete: completeEnvName},
		{Tag: tagSecret, Kind: KindSecret, Label: secretLabel, Hint: secretHint},
	}

	form := NewForm(title, fields, func(f *Form) error {
		a := accountFromForm(f)
		if strings.TrimSpace(a.ID) == "" {
			return fmt.Errorf("ID is required")
		}
		if strings.TrimSpace(a.Host) == "" {
			return fmt.Errorf("host is required")
		}
		if a.Port < 1 || a.Port > 65535 {
			return fmt.Errorf("port must be between 1 and 65535")
		}
		if strings.TrimSpace(a.Auth.Username) == "" && a.Auth.Type != "agent" {
			return fmt.Errorf("username is required for %s auth", a.Auth.Type)
		}
		if a.Auth.Type == "key" && strings.TrimSpace(a.Auth.KeyPath) == "" {
			return fmt.Errorf("key_path is required for key auth")
		}
		if a.Auth.Type == "password" {
			typed := f.byTag(tagSecret).Value
			stored := m.secrets.Accounts[a.ID].Password
			if strings.TrimSpace(a.Auth.PasswordEnv) == "" && typed == "" && stored == "" {
				return fmt.Errorf("enter a password (stored in secrets.yaml) or set password_env")
			}
		}

		// Reject duplicate IDs, and reject renames that would orphan projects.
		for i, existing := range m.cfg.Accounts {
			if existing.ID == a.ID && i != idx {
				return fmt.Errorf("account %q already exists", a.ID)
			}
		}
		if idx >= 0 {
			old := m.cfg.Accounts[idx]
			if old.ID != a.ID {
				var refs []string
				for _, p := range m.cfg.Projects {
					if p.AccountID == old.ID {
						refs = append(refs, p.ID)
					}
				}
				if len(refs) > 0 {
					return fmt.Errorf("renaming %q would break projects %s — update those projects first", old.ID, strings.Join(refs, ", "))
				}
				// Carry the stored secret over to the new ID.
				if stored, ok := m.secrets.Accounts[old.ID]; ok {
					delete(m.secrets.Accounts, old.ID)
					m.secrets.Accounts[a.ID] = stored
				}
			}
		}

		if idx >= 0 && idx < len(m.cfg.Accounts) {
			m.cfg.Accounts[idx] = a
		} else {
			m.cfg.Accounts = append(m.cfg.Accounts, a)
		}

		// A non-empty secret field is written to secrets.yaml on save;
		// an empty field keeps the stored value (like a web password form).
		if typed := f.byTag(tagSecret).Value; typed != "" {
			cur := m.secrets.Accounts[a.ID]
			switch a.Auth.Type {
			case "password":
				cur.Password = typed
			case "key":
				cur.Passphrase = typed
			}
			m.secrets.Accounts[a.ID] = cur
		}
		return nil
	}, func(f *Form) tea.Cmd {
		// Connection test: typed secret wins, then the stored one, then error.
		a := accountFromForm(f)
		typed := f.byTag(tagSecret).Value
		stored := m.secrets.Accounts[a.ID]
		fieldLabel := f.byTag(tagSecret).Label
		prompt := func(label string) (string, error) {
			if typed != "" {
				return typed, nil
			}
			if strings.Contains(label, "passphrase") && stored.Passphrase != "" {
				return stored.Passphrase, nil
			}
			if stored.Password != "" {
				return stored.Password, nil
			}
			return "", fmt.Errorf("no secret available — type the %s field or set password_env", fieldLabel)
		}
		return func() tea.Msg {
			return testResultMsg{text: testAccount(a, prompt)}
		}
	})
	form.onSecretClear = func(f *Form) {
		accID := f.byTag(tagID).Value
		cur, ok := m.secrets.Accounts[accID]
		if !ok {
			f.setValue(f.byTagIdx(tagSecret), "")
			f.byTag(tagSecret).Hint = "no stored secret"
			return
		}
		switch f.byTag(tagAuth).Value {
		case "password":
			cur.Password = ""
		case "key":
			cur.Passphrase = ""
		}
		if cur.Password == "" && cur.Passphrase == "" {
			delete(m.secrets.Accounts, accID)
		} else {
			m.secrets.Accounts[accID] = cur
		}
		f.setValue(f.byTagIdx(tagSecret), "")
		f.byTag(tagSecret).Hint = "stored secret cleared"
	}
	m.form = form
}

func accountFromForm(f *Form) config.Account {
	port, err := strconv.Atoi(f.byTag(tagPort).Value)
	if err != nil {
		port = 0
	}
	protocol := f.byTag(tagProtocol).Value
	if port < 1 {
		port = defaultPort(protocol)
	}
	return config.Account{
		ID:       strings.TrimSpace(f.byTag(tagID).Value),
		Name:     strings.TrimSpace(f.byTag(tagName).Value),
		Protocol: protocol,
		Host:     strings.TrimSpace(f.byTag(tagHost).Value),
		Port:     port,
		TLSMode:  f.byTag(tagTLS).Value,
		Auth: config.Auth{
			Type:             f.byTag(tagAuth).Value,
			Username:         strings.TrimSpace(f.byTag(tagUser).Value),
			KeyPath:          strings.TrimSpace(f.byTag(tagKey).Value),
			PassphrasePrompt: f.byTag(tagPassHint).Value == "1",
			PasswordEnv:      strings.TrimSpace(f.byTag(tagPassEnv).Value),
		},
	}
}

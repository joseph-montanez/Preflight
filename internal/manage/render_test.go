package manage

import (
	"fmt"
	"os"
	"regexp"
	"testing"

	"godeploy/internal/config"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRenderSmoke(t *testing.T) {
	cfg := &config.Config{
		Accounts: []config.Account{
			{ID: "prod-web", Name: "Production Web 01", Protocol: "sftp", Host: "ssh.example.com", Port: 22, Auth: config.Auth{Type: "key", Username: "deploy", KeyPath: "~/.ssh/id_ed25519"}},
			{ID: "legacy-ftps", Name: "Legacy FTPS", Protocol: "ftps", Host: "ftp.example.com", Port: 21, TLSMode: "explicit", Auth: config.Auth{Type: "password", Username: "deploy", PasswordEnv: "GODEPLOY_FTP_PASSWORD"}},
		},
		Projects: []config.Project{
			{ID: "api-backend", Name: "Backend API", AccountID: "prod-web", LocalRoot: "/Users/dev/code/api-backend", RemoteRoot: "/var/www/api-backend", DefaultDiffMode: "text"},
		},
	}
	m := send(New(cfg, "/home/dev/.config/godeploy/config.yaml"), tea.WindowSizeMsg{Width: 110, Height: 30})

	ansi := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	fmt.Println("=== MAIN VIEW ===")
	fmt.Println(ansi.ReplaceAllString(m.View(), ""))

	m = sendKey(m, "enter") // edit account
	fmt.Println("=== ACCOUNT FORM ===")
	fmt.Println(ansi.ReplaceAllString(m.View(), ""))

	m = sendKey(m, "esc")
	m = sendKey(m, "tab") // projects
	m = sendKey(m, "enter")
	fmt.Println("=== PROJECT FORM ===")
	fmt.Println(ansi.ReplaceAllString(m.View(), ""))

	_ = os.Stdout
}

package remotebrowser

import (
	"path/filepath"
	"testing"

	"godeploy/internal/config"
)

func TestResolveLocalPath(t *testing.T) {
	mappings := []config.PathMapping{
		{LocalPath: "public", RemotePath: "/var/www/api-backend"},
		{LocalPath: "deploy/config", RemotePath: "/etc/api-backend"},
	}
	localRoot := filepath.FromSlash("/home/dev/code/api-backend")

	cases := []struct {
		name      string
		remote    string
		wantLocal string
		wantOK    bool
	}{
		{"exact mapping root (dir)", "/var/www/api-backend", filepath.Join(localRoot, "public"), true},
		{"file under mapping", "/var/www/api-backend/index.php", filepath.Join(localRoot, "public", "index.php"), true},
		{"nested dir under mapping", "/var/www/api-backend/assets", filepath.Join(localRoot, "public", "assets"), true},
		{"nested file in subdir", "/var/www/api-backend/assets/logo.png", filepath.Join(localRoot, "public", "assets", "logo.png"), true},
		{"second mapping", "/etc/api-backend/nginx.conf", filepath.Join(localRoot, "deploy", "config", "nginx.conf"), true},
		{"unmapped", "/etc/nginx/conf.d/default.conf", "", false},
		{"root is unmapped", "/", "", false},
		{"sibling prefix not a match", "/var/www/api-backend2", "", false},
	}
	for _, c := range cases {
		got, ok := ResolveLocalPath(c.remote, mappings, localRoot)
		if ok != c.wantOK || (ok && got != c.wantLocal) {
			t.Fatalf("%s: ResolveLocalPath(%q) = (%q, %v), want (%q, %v)",
				c.name, c.remote, got, ok, c.wantLocal, c.wantOK)
		}
	}
}

func TestResolveLocalPathLongestPrefixWins(t *testing.T) {
	mappings := []config.PathMapping{
		{LocalPath: "site", RemotePath: "/srv/web"},
		{LocalPath: "site/assets", RemotePath: "/srv/web/assets"},
	}
	localRoot := filepath.FromSlash("/home/dev/code/proj")

	got, ok := ResolveLocalPath("/srv/web/assets/img/logo.png", mappings, localRoot)
	if !ok || got != filepath.Join(localRoot, "site", "assets", "img", "logo.png") {
		t.Fatalf("ResolveLocalPath = (%q, %v), want the most specific mapping", got, ok)
	}
}

func TestResolveLocalPathRootMapping(t *testing.T) {
	// A project with only remote_root behaves like a mapping from the root.
	mappings := []config.PathMapping{{RemotePath: "/srv/app"}}
	localRoot := filepath.FromSlash("/home/dev/code/proj")

	got, ok := ResolveLocalPath("/srv/app/x.php", mappings, localRoot)
	if !ok || got != filepath.Join(localRoot, "x.php") {
		t.Fatalf("ResolveLocalPath = (%q, %v), want local_root/x.php, true", got, ok)
	}
}

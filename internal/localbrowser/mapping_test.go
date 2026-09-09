package localbrowser

import (
	"path/filepath"
	"testing"

	"preflight/internal/config"
)

func TestResolveRemotePath(t *testing.T) {
	mappings := []config.PathMapping{
		{LocalPath: "public", RemotePath: "/var/www/public"},  // exact dir
		{LocalPath: "public/img", RemotePath: "/var/www/img"}, // nested, longest prefix wins
		{LocalPath: "public/img/cache", RemotePath: "/srv/cache"},
		{LocalPath: "static\\files", RemotePath: "/etc/files"}, // backslash local path
	}
	root := filepath.Clean("/home/user/app") // normalized to slash by cleanLocal

	cases := []struct {
		name   string
		local  string
		want   string
		wantOK bool
	}{
		{"exact mapping dir", "/home/user/app/public", "/var/www/public", true},
		{"suffix below mapping", "/home/user/app/public/img/logo.png", "/var/www/img/logo.png", true},
		{"longest prefix wins", "/home/user/app/public/img/cache/x.png", "/srv/cache/x.png", true},
		{"deepest wins over parent", "/home/user/app/public/img/deep/y.txt", "/var/www/img/deep/y.txt", true},
		{"backslash mapping local", "/home/user/app/static/files/app.conf", "/etc/files/app.conf", true},
		{"outside every mapping", "/home/user/app/private/key.pem", "", false},
		{"outside root unmapped", "/home/user/other/file.txt", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ResolveRemotePath(tc.local, mappings, root)
			if ok != tc.wantOK {
				t.Fatalf("ResolveRemotePath(%q) ok = %v, want %v", tc.local, ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Fatalf("ResolveRemotePath(%q) = %q, want %q", tc.local, got, tc.want)
			}
		})
	}
}

func TestResolveRemotePathBackslashInput(t *testing.T) {
	mappings := []config.PathMapping{{LocalPath: "public", RemotePath: "/var/www/public"}}
	got, ok := ResolveRemotePath(`C:\dev\app\public\index.html`, mappings, `C:\dev\app`)
	if !ok || got != "/var/www/public/index.html" {
		t.Fatalf("backslash input: got %q ok=%v, want /var/www/public/index.html true", got, ok)
	}
}

func TestResolveRemotePathRootMappingOnly(t *testing.T) {
	mappings := []config.PathMapping{{RemotePath: "/srv/app"}}
	root := filepath.Clean("/home/user/app")
	for _, tc := range []struct{ local, want string }{
		{"/home/user/app", "/srv/app"},
		{"/home/user/app/sub/f.txt", "/srv/app/sub/f.txt"},
	} {
		got, ok := ResolveRemotePath(tc.local, mappings, root)
		if !ok || got != tc.want {
			t.Fatalf("ResolveRemotePath(%q) = %q ok=%v, want %q", tc.local, got, ok, tc.want)
		}
	}
}

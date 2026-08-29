package remote

import "testing"

func TestJoinRemote(t *testing.T) {
	cases := []struct{ base, name, want string }{
		{"", "etc", "etc"},
		{"/", "etc", "/etc"},
		{"/var/www", "api", "/var/www/api"},
		{"/var/www/", "api", "/var/www/api"},
	}
	for _, c := range cases {
		if got := JoinRemote(c.base, c.name); got != c.want {
			t.Fatalf("JoinRemote(%q, %q) = %q, want %q", c.base, c.name, got, c.want)
		}
	}
}

func TestSplitRemote(t *testing.T) {
	cases := []struct{ in, base, last string }{
		{"", "/", ""},
		{"/", "/", ""},
		{"/var/www/", "/var/www", ""},
		{"/var/www/api", "/var/www", "api"},
		{"etc", "", "etc"},
	}
	for _, c := range cases {
		base, last := SplitRemote(c.in)
		if base != c.base || last != c.last {
			t.Fatalf("SplitRemote(%q) = (%q, %q), want (%q, %q)", c.in, base, last, c.base, c.last)
		}
	}
}

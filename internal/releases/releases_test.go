package releases

import "testing"

func TestSemanticOrdering(t *testing.T) {
	ordered := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.10.0", "2.0.0", "999999999999999999999999.0.0"}
	for i, s := range ordered {
		v, e := Parse(s)
		if e != nil {
			t.Fatal(e)
		}
		for j, w := range ordered {
			other, _ := Parse(w)
			c := v.Compare(other)
			if i < j && c >= 0 || i > j && c <= 0 || i == j && c != 0 {
				t.Fatalf("%s vs %s: %d", s, w, c)
			}
		}
	}
	a, _ := Parse("v1.2.3+one")
	b, _ := Parse("1.2.3+two")
	if a.Compare(b) != 0 {
		t.Fatal("build metadata affects precedence")
	}
	for _, s := range []string{"1.2", "01.2.3", "1.2.3-01", "1.2.3-a..b", "1.2.3+.", "1.2.3-", "../1.2.3"} {
		if _, e := Parse(s); e == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	for _, s := range []string{"0.1.0-dev", "v0.0.0-20261001000000-abcdef123456", "v1.2.4-0.20261001000000-abcdef123456"} {
		v, e := Parse(s)
		if e != nil || !v.Development() {
			t.Fatalf("not recognized as development: %s", s)
		}
	}
}
func TestAssetSelection(t *testing.T) {
	for _, row := range []struct{ os, arch, want string }{{"linux", "amd64", "easy-sso-1.2.3-linux-amd64.tar.gz"}, {"darwin", "arm64", "easy-sso-1.2.3-darwin-arm64.tar.gz"}, {"windows", "amd64", "easy-sso-1.2.3-windows-amd64.zip"}} {
		got, e := Asset("v1.2.3", row.os, row.arch)
		if e != nil || got != row.want {
			t.Fatalf("%s %v", got, e)
		}
	}
	if _, e := Asset("1.2.3", "windows", "arm64"); e == nil {
		t.Fatal("unsupported platform accepted")
	}
}

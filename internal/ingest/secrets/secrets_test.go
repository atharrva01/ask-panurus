package secrets

import "testing"

func TestFind(t *testing.T) {
	dirty := map[string]string{
		"private-key":    "x := `-----BEGIN EC PRIVATE KEY-----\nMHc...`",
		"aws-access-key": "id = AKIAIOSFODNN7EXAMPLE",
		"github-token":   "GH_TOKEN=ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789",
	}
	for want, s := range dirty {
		if got := Find(s); len(got) != 1 || got[0] != want {
			t.Errorf("Find(%q) = %v, want [%s]", s, got, want)
		}
	}
	clean := []string{
		"-----BEGIN CERTIFICATE-----", // certificates are public
		"func (t *Token) Secret() []byte",
		`password := os.Getenv("DB_PASSWORD")`,
	}
	for _, s := range clean {
		if got := Find(s); got != nil {
			t.Errorf("Find(%q) = %v, want nil", s, got)
		}
	}
}

func TestPathRules(t *testing.T) {
	if !SkipDir("testdata") || !SkipDir("msp") || SkipDir("token") {
		t.Error("dir rules wrong")
	}
	if !SkipFile("a/b/cert.PEM") || SkipFile("a/b/key.go") {
		t.Error("file rules wrong")
	}
}

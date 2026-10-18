package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRun(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module github.com/LFDT-Panurus/panurus\n\ngo 1.27\n")
	write(t, root, "token/token.go", "package token\n\n// Token is a token.\ntype Token struct{}\n")
	write(t, root, "x/evm/go.mod", "module github.com/LFDT-Panurus/panurus/x/evm\n")
	write(t, root, "x/evm/keys/keys.go", "package keys\n\nfunc Key() string { return \"k\" }\n")
	write(t, root, "docs/README.md", "# Docs\n\nHello.\n")
	write(t, root, "Makefile", "unit-tests:\n\tgo test ./...\n")
	write(t, root, "token/testdata/gen.go", "package testdata\n\nfunc Gen() {}\n")
	write(t, root, "token/msp/IssuerSecretKey", "secret")
	write(t, root, "token/cert.pem", "-----BEGIN CERTIFICATE-----\n")
	write(t, root, "token/leak.go", "package token\n\nconst k = `-----BEGIN PRIVATE KEY-----`\n")

	cs, st, err := Run(Options{Root: root, Repo: "LFDT-Panurus/panurus", Commit: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]string{}
	for _, c := range cs {
		byID[c.ID] = c.Package
		if c.CommitSHA != "abc123" || c.ContentHash == "" {
			t.Errorf("%s missing commit or hash", c.ID)
		}
		if strings.Contains(c.Path, "testdata") || strings.Contains(c.Path, "msp") {
			t.Errorf("excluded path indexed: %s", c.Path)
		}
	}
	if pkg := byID["code:token/token.go:Token#0"]; pkg != "github.com/LFDT-Panurus/panurus/token" {
		t.Errorf("token package = %q", pkg)
	}
	if pkg := byID["code:x/evm/keys/keys.go:Key#0"]; pkg != "github.com/LFDT-Panurus/panurus/x/evm/keys" {
		t.Errorf("nested module package = %q (ids %v)", pkg, byID)
	}
	if _, ok := byID["doc:docs/README.md:Docs#0"]; !ok {
		t.Errorf("doc chunk missing: %v", byID)
	}
	if _, ok := byID["build:Makefile:target unit-tests#0"]; !ok {
		t.Errorf("build chunk missing: %v", byID)
	}
	if len(st.SecretDrops) != 1 || !strings.HasPrefix(st.SecretDrops[0], "token/leak.go:") {
		t.Errorf("secret drops = %v", st.SecretDrops)
	}
	if strings.Contains(strings.Join(st.SecretDrops, ""), "BEGIN") {
		t.Error("secret drop log must not contain content")
	}
}

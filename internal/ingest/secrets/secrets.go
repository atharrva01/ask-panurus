// Package secrets keeps credentials and key material out of the index
// (FR-3b). It has two layers: path rules that stop sensitive files from
// being read at all, and content rules that drop any chunk that still looks
// like it holds a secret.
//
// The content rules are a small built-in subset of the gitleaks rule set,
// covering the formats that plausibly appear in this repository. Swapping in
// the full gitleaks detector only needs a new Scanner.
package secrets

import (
	"path"
	"regexp"
	"strings"
)

// excludedDirs are never descended into. msp and keystore directories hold
// Fabric and idemix key material, often in files without extensions. "keys"
// is deliberately absent: in Panurus it names Go packages (ledger key
// encoding), not key material.
var excludedDirs = map[string]bool{
	".git":          true,
	"vendor":        true,
	"node_modules":  true,
	"testdata":      true,
	"msp":           true,
	"keystore":      true,
	"crypto-config": true,
}

var excludedExts = map[string]bool{
	".pem": true, ".key": true, ".crt": true, ".cer": true, ".der": true,
	".p12": true, ".pfx": true, ".jks": true, ".keystore": true,
}

// SkipDir reports whether a directory with this base name must not be read.
func SkipDir(name string) bool { return excludedDirs[name] }

// SkipFile reports whether a repository-relative file path must not be read.
func SkipFile(p string) bool {
	return excludedExts[strings.ToLower(path.Ext(p))]
}

// Rule is one content pattern.
type Rule struct {
	ID string
	re *regexp.Regexp
}

var rules = []Rule{
	{"private-key", regexp.MustCompile(`-----BEGIN ((RSA|EC|DSA|OPENSSH|ENCRYPTED) )?PRIVATE KEY( BLOCK)?-----`)},
	{"aws-access-key", regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"github-token", regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[0-9A-Za-z]{36}\b|\bgithub_pat_[0-9A-Za-z_]{82}\b`)},
	{"slack-token", regexp.MustCompile(`\bxox[baprs]-[0-9A-Za-z-]{10,}\b`)},
	{"gcp-api-key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"stripe-key", regexp.MustCompile(`\b(sk|rk)_live_[0-9A-Za-z]{24,}\b`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)},
}

// Find returns the IDs of the rules that match s, or nil if it is clean.
func Find(s string) []string {
	var hits []string
	for _, r := range rules {
		if r.re.MatchString(s) {
			hits = append(hits, r.ID)
		}
	}
	return hits
}

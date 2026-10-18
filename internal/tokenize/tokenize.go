// Package tokenize produces the code-aware keyword tokens used on both
// sides of keyword search: when chunks are indexed and when a question is
// searched. Both must use the same function or exact identifiers stop
// matching.
package tokenize

import (
	"strings"
	"unicode"
)

// Tokens splits s into lowercase search tokens. Every identifier is kept
// whole and, if it has more than one part, also split at snake_case,
// camelCase and letter/digit boundaries:
//
//	TokenStateFactory -> tokenstatefactory token state factory
//	EIP712Digest      -> eip712digest eip 712 digest
//	HTTPServer        -> httpserver http server
//
// Tokens shorter than two characters are dropped.
func Tokens(s string) []string {
	var out []string
	add := func(t string) {
		if len(t) >= 2 {
			out = append(out, strings.ToLower(t))
		}
	}
	for _, word := range strings.FieldsFunc(s, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
	}) {
		word = strings.Trim(word, "_")
		parts := splitIdent(word)
		add(strings.ReplaceAll(word, "_", ""))
		if len(parts) > 1 {
			for _, p := range parts {
				add(p)
			}
		}
	}
	return out
}

// Join returns Tokens(s) as one space-separated string.
func Join(s string) string { return strings.Join(Tokens(s), " ") }

func splitIdent(w string) []string {
	var parts []string
	for _, seg := range strings.Split(w, "_") {
		rs := []rune(seg)
		start := 0
		for i := 1; i < len(rs); i++ {
			prev, cur := rs[i-1], rs[i]
			next := rune(0)
			if i+1 < len(rs) {
				next = rs[i+1]
			}
			boundary := (unicode.IsLower(prev) && unicode.IsUpper(cur)) ||
				(unicode.IsUpper(prev) && unicode.IsUpper(cur) && unicode.IsLower(next)) ||
				(unicode.IsLetter(prev) && unicode.IsDigit(cur)) ||
				(unicode.IsDigit(prev) && unicode.IsLetter(cur))
			if boundary {
				parts = append(parts, string(rs[start:i]))
				start = i
			}
		}
		if start < len(rs) {
			parts = append(parts, string(rs[start:]))
		}
	}
	return parts
}

// Package tokenize produces the code-aware keyword tokens used on both
// sides of keyword search: when chunks are indexed and when a question is
// searched. Both must use the same function or exact identifiers stop
// matching.
package tokenize

import (
	"strings"
	"unicode"

	"github.com/kljensen/snowball/english"
)

// Tokens splits s into lowercase search tokens.
//
// An identifier with several parts is kept whole and unstemmed, so an exact
// identifier in a question matches strongly, and each part is also emitted
// stemmed. A single word is emitted stemmed only. Splitting happens at
// snake_case, camelCase and letter/digit boundaries:
//
//	TokenStateFactory -> tokenstatefactory token state factori
//	EIP712Digest      -> eip712digest eip 712 digest
//	endorsements      -> endors
//
// Tokens shorter than two characters are dropped.
func Tokens(s string) []string {
	var out []string
	add := func(t string, stem bool) {
		t = strings.ToLower(t)
		if stem {
			t = english.Stem(t, false)
		}
		if len(t) >= 2 {
			out = append(out, t)
		}
	}
	for _, word := range strings.FieldsFunc(s, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
	}) {
		word = strings.Trim(word, "_")
		parts := splitIdent(word)
		if len(parts) == 1 {
			add(word, true)
			continue
		}
		add(strings.ReplaceAll(word, "_", ""), false)
		for _, p := range parts {
			add(p, true)
		}
	}
	return out
}

// Join returns Tokens(s) as one space-separated string.
func Join(s string) string { return strings.Join(Tokens(s), " ") }

// stopwords are dropped from questions only. They stay in the index, where
// inverse document frequency already gives them little weight.
var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be by can could do does did doe for from has have how
		i if in into is it its me my of on or should so than that the their them then there these this
		those to was we were what when where which while who why will with would you your
		panurus use used using work way`) {
		for _, t := range Tokens(w) {
			stopwords[t] = true
		}
	}
}

// QueryTerms returns the distinct tokens of a question, without stopwords,
// in order of first appearance.
func QueryTerms(q string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range Tokens(q) {
		if stopwords[t] || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func splitIdent(w string) []string {
	var parts []string
	for _, seg := range strings.Split(w, "_") {
		if seg == "" {
			continue
		}
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

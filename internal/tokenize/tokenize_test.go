package tokenize

import (
	"strings"
	"testing"
)

func TestJoin(t *testing.T) {
	cases := map[string]string{
		"TokenStateFactory":          "tokenstatefactory token state factori",
		"EIP712Digest":               "eip712digest eip 712 digest",
		"HTTPServer":                 "httpserver http server",
		"spent_tracking":             "spenttracking spent track",
		"(*Driver).New":              "driver new",
		"token/services/network/evm": "token servic network evm",
		"a x := y":                   "",
		"endorsements endorsed":      "endors endors",
	}
	for in, want := range cases {
		if got := Join(in); got != want {
			t.Errorf("Join(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQueryTermsMatchIndexTokens(t *testing.T) {
	got := strings.Join(QueryTerms("How does the EVM driver collect endorsements? Does the driver use EIP712Digest?"), " ")
	want := "evm driver collect endors eip712digest eip 712 digest"
	if got != want {
		t.Errorf("QueryTerms = %q, want %q", got, want)
	}
	// A question word and the indexed identifier part must meet on the same
	// stem, or keyword search silently misses.
	idx := Tokens("func (i *Initiator) CollectEndorsements()")
	for _, q := range QueryTerms("collecting endorsement") {
		found := false
		for _, t := range idx {
			found = found || t == q
		}
		if !found {
			t.Errorf("query term %q not among index tokens %v", q, idx)
		}
	}
}

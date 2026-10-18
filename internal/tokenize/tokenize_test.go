package tokenize

import "testing"

func TestJoin(t *testing.T) {
	cases := map[string]string{
		"TokenStateFactory":             "tokenstatefactory token state factory",
		"EIP712Digest":                  "eip712digest eip 712 digest",
		"HTTPServer":                    "httpserver http server",
		"spent_tracking":                "spenttracking spent tracking",
		"(*Driver).New":                 "driver new",
		"token/services/network/evm":    "token services network evm",
		"a x := y":                      "",
		"zkatdlog nogh v1":              "zkatdlog nogh v1",
		"How does the EVM driver work?": "how does the evm driver work",
	}
	for in, want := range cases {
		if got := Join(in); got != want {
			t.Errorf("Join(%q) = %q, want %q", in, got, want)
		}
	}
}

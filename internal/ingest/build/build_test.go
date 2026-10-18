package build

import (
	"strings"
	"testing"

	"github.com/atharrva01/ask-panurus/internal/chunk"
)

func names(cs []chunk.Chunk) string {
	var n []string
	for _, c := range cs {
		n = append(n, c.Symbol)
	}
	return strings.Join(n, "|")
}

func TestMakefile(t *testing.T) {
	src := `GO ?= go
TOP := $(shell pwd)

.PHONY: unit-tests
# Run unit tests.
unit-tests:
	$(GO) test ./...

integration-tests-evm: docker-images
	make -C integration evm
`
	cs := Chunk("evm.mk", []byte(src))
	if got := names(cs); got != "preamble|target unit-tests|target integration-tests-evm" {
		t.Fatalf("sections = %s", got)
	}
	ut := cs[1]
	if ut.StartLine != 5 || !strings.HasPrefix(ut.Content, "# Run unit tests.") {
		t.Errorf("unit-tests = %d %q", ut.StartLine, ut.Content)
	}
	if !strings.Contains(cs[0].Content, ".PHONY") {
		t.Error(".PHONY line should stay in the preamble")
	}
}

func TestWorkflow(t *testing.T) {
	src := `name: Tests
on: [push]
jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - run: make unit-tests
  evm:
    runs-on: ubuntu-latest
    steps:
      - run: make integration-tests-evm
`
	cs := Chunk(".github/workflows/tests.yml", []byte(src))
	if got := names(cs); got != "workflow|job unit|job evm" {
		t.Fatalf("sections = %s", got)
	}
	if cs[2].StartLine != 8 || cs[2].EndLine != 11 {
		t.Errorf("evm job lines = %d-%d", cs[2].StartLine, cs[2].EndLine)
	}
}

func TestProto(t *testing.T) {
	src := `syntax = "proto3";
package v1;

// Action is a token action.
message Action {
  oneof kind {
    Issue issue = 1;
  }
}

enum Kind { A = 0; }
`
	cs := Chunk("token/driver/protos/v1/request.proto", []byte(src))
	if got := names(cs); got != "header|message Action|enum Kind" {
		t.Fatalf("sections = %s", got)
	}
	if cs[1].StartLine != 4 || cs[1].EndLine != 9 {
		t.Errorf("message lines = %d-%d", cs[1].StartLine, cs[1].EndLine)
	}
}

func TestIsBuildFile(t *testing.T) {
	yes := []string{"Makefile", "evm.mk", ".github/workflows/tests.yml", "ci/scripts/x.sh", "ci/external-builders/golang/bin/build", "go.mod", "token/driver/protos/v1/pp.proto"}
	no := []string{"docs/x.md", "token/x.go", "ci/govulncheck-allowlist.txt", "mkdocs.yml"}
	for _, p := range yes {
		if !IsBuildFile(p) {
			t.Errorf("%s should be a build file", p)
		}
	}
	for _, p := range no {
		if IsBuildFile(p) {
			t.Errorf("%s should not be a build file", p)
		}
	}
}

// Package build chunks the files behind how-to questions: Makefiles by
// target, GitHub workflows by job, .proto files by message or service, and
// scripts and other build files as whole files (PRD section 8, "Build and CI
// chunker").
package build

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/atharrva01/ask-panurus/internal/chunk"
)

// maxWholeFileLines is the size above which a whole-file chunk is split.
const maxWholeFileLines = 120

// IsBuildFile reports whether p (repository-relative) is handled by Chunk.
func IsBuildFile(p string) bool {
	base := path.Base(p)
	switch {
	case base == "Makefile", strings.HasSuffix(base, ".mk"):
		return true
	case strings.HasPrefix(p, ".github/workflows/") && (strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".yaml")):
		return true
	case strings.HasSuffix(base, ".proto"), strings.HasSuffix(base, ".sh"):
		return true
	case base == "go.mod", strings.HasPrefix(base, "Dockerfile"):
		return true
	case strings.HasPrefix(p, "ci/") && path.Ext(base) == "":
		return true // extensionless CI helper scripts
	}
	return false
}

// Chunk splits one build file.
func Chunk(p string, src []byte) []chunk.Chunk {
	lines := strings.Split(strings.TrimRight(string(src), "\n"), "\n")
	var sections []section
	base := path.Base(p)
	switch {
	case base == "Makefile", strings.HasSuffix(base, ".mk"):
		sections = makefileSections(lines)
	case strings.HasPrefix(p, ".github/workflows/"):
		sections = workflowSections(src, lines)
	case strings.HasSuffix(base, ".proto"):
		sections = protoSections(lines)
	}
	if sections == nil {
		sections = wholeFile(lines)
	}

	var out []chunk.Chunk
	for _, s := range sections {
		body := strings.Join(lines[s.start-1:s.end], "\n")
		if strings.TrimSpace(body) == "" {
			continue
		}
		c := chunk.Chunk{
			Kind:      chunk.KindBuild,
			Path:      p,
			Symbol:    s.name,
			StartLine: s.start,
			EndLine:   s.end,
			Content:   body + "\n",
		}
		c.EmbedText = fmt.Sprintf("file: %s\nsection: %s\n\n%s", p, s.name, c.Content)
		c.SetKey(fmt.Sprintf("build:%s:%s", p, s.name))
		out = append(out, c)
	}
	return out
}

type section struct {
	name       string
	start, end int // 1-based, inclusive
}

var makeTarget = regexp.MustCompile(`^([A-Za-z0-9_%$(){}./-][^:=#]*?)\s*::?(\s|$|[^=])`)

// makefileSections makes one section per rule. A rule starts at the comment
// block directly above its target line and runs until the next rule.
// Anything before the first rule (variables, includes) is a "preamble".
func makefileSections(lines []string) []section {
	type rule struct {
		name  string
		start int
	}
	var rules []rule
	for i, l := range lines {
		if l == "" || l[0] == '\t' || l[0] == ' ' || l[0] == '#' || l[0] == '.' {
			continue
		}
		if strings.Contains(l, ":=") || strings.Contains(l, "?=") || strings.Contains(l, "+=") || strings.Contains(l, "!=") {
			continue
		}
		m := makeTarget.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		start := i + 1
		for start > 1 && strings.HasPrefix(lines[start-2], "#") {
			start--
		}
		rules = append(rules, rule{strings.Join(strings.Fields(m[1]), " "), start})
	}
	if len(rules) == 0 {
		return nil
	}
	var out []section
	if rules[0].start > 1 {
		out = append(out, section{"preamble", 1, rules[0].start - 1})
	}
	for i, r := range rules {
		end := len(lines)
		if i+1 < len(rules) {
			end = rules[i+1].start - 1
		}
		out = append(out, section{"target " + r.name, r.start, end})
	}
	return out
}

// workflowSections makes one section per job, plus a header section for the
// workflow name and triggers.
func workflowSections(src []byte, lines []string) []section {
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil || len(root.Content) == 0 {
		return nil
	}
	doc := root.Content[0]
	var jobs *yaml.Node
	jobsLine := 0
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value == "jobs" {
			jobs, jobsLine = doc.Content[i+1], doc.Content[i].Line
		}
	}
	if jobs == nil || jobs.Kind != yaml.MappingNode || len(jobs.Content) == 0 {
		return nil
	}
	out := []section{{"workflow", 1, jobsLine}}
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		start := jobs.Content[i].Line
		end := len(lines)
		if i+2 < len(jobs.Content) {
			end = jobs.Content[i+2].Line - 1
		}
		out = append(out, section{"job " + jobs.Content[i].Value, start, end})
	}
	return out
}

var protoBlock = regexp.MustCompile(`^\s*(message|service|enum)\s+(\w+)`)

// protoSections makes one section per top-level message, service or enum,
// including its leading comments. The header (syntax, package, imports,
// options) is its own section.
func protoSections(lines []string) []section {
	var out []section
	depth := 0
	var cur *section
	firstBlock := 0
	for i, l := range lines {
		n := i + 1
		if depth == 0 && cur == nil {
			if m := protoBlock.FindStringSubmatch(l); m != nil {
				start := n
				for start > 1 && strings.HasPrefix(strings.TrimSpace(lines[start-2]), "//") {
					start--
				}
				if firstBlock == 0 {
					firstBlock = start
				}
				cur = &section{m[1] + " " + m[2], start, n}
			}
		}
		depth += strings.Count(l, "{") - strings.Count(l, "}")
		if cur != nil && depth == 0 && strings.Contains(l, "}") {
			cur.end = n
			out = append(out, *cur)
			cur = nil
		}
	}
	if cur != nil {
		cur.end = len(lines)
		out = append(out, *cur)
	}
	if firstBlock > 1 {
		out = append([]section{{"header", 1, firstBlock - 1}}, out...)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func wholeFile(lines []string) []section {
	var out []section
	for start := 1; start <= len(lines); start += maxWholeFileLines {
		end := min(start+maxWholeFileLines-1, len(lines))
		out = append(out, section{"file", start, end})
	}
	return out
}

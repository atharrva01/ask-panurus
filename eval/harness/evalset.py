"""Load and validate the eval set (PRD section 11).

    python3 eval/harness/evalset.py eval/v1.yaml --snapshot .data/panurus@6cf946a9b519

Validation fails on anything that would make a metric silently wrong: a gold
path missing at the pinned commit, a line range outside its file, a snapshot
taken at a different commit, or an answerable question with no gold label.
"""

import argparse
import collections
import os
import sys
from dataclasses import dataclass, field

import yaml

TYPES = ("locate", "explain", "compare", "why", "how-to", "unanswerable")
SOURCE_KINDS = ("code", "doc", "build", "test", "issues")


@dataclass
class Range:
    path: str
    start: int
    end: int

    def overlaps(self, path, start, end):
        return path == self.path and start <= self.end and end >= self.start


@dataclass
class Question:
    id: str
    type: str
    question: str
    answerable: bool
    gold: list = field(default_factory=list)
    gold_refs: list = field(default_factory=list)
    require_all: bool = False
    needs: list = field(default_factory=list)
    required_facts: list = field(default_factory=list)
    status: str = "draft"
    raw: dict = field(default_factory=dict)


@dataclass
class EvalSet:
    version: int
    repo: str
    commit: str
    questions: list


def load(path):
    with open(path) as f:
        doc = yaml.safe_load(f)
    qs = []
    for q in doc["questions"]:
        qs.append(Question(
            id=q["id"],
            type=q["type"],
            question=q["question"],
            answerable=bool(q["answerable"]),
            gold=[Range(g["path"], g["lines"][0], g["lines"][1]) for g in q.get("gold_ranges") or []],
            gold_refs=q.get("gold_refs") or [],
            require_all=bool(q.get("require_all_ranges", False)),
            needs=q.get("needs_sources") or [],
            required_facts=q.get("required_facts") or [],
            status=q.get("status", "draft"),
            raw=q,
        ))
    return EvalSet(doc["version"], doc["panurus_repo"], doc["panurus_commit"], qs)


def validate(es, snapshot=None):
    """Returns a list of problems; empty means valid."""
    problems = []
    seen = set()
    if snapshot:
        marker = os.path.join(snapshot, ".ask-panurus-commit")
        try:
            got = open(marker).read().strip()
        except OSError:
            problems.append(f"snapshot {snapshot} has no .ask-panurus-commit")
            got = None
        if got and got != es.commit:
            problems.append(f"snapshot is at {got[:12]} but the eval set is pinned to {es.commit[:12]}")
    line_counts = {}
    for q in es.questions:
        where = f"{q.id}:"
        if q.id in seen:
            problems.append(f"{where} duplicate id")
        seen.add(q.id)
        if q.type not in TYPES:
            problems.append(f"{where} unknown type {q.type!r}")
        for k in q.needs:
            if k not in SOURCE_KINDS:
                problems.append(f"{where} unknown source kind {k!r}")
        if (q.type == "unanswerable") == q.answerable:
            problems.append(f"{where} type and answerable flag disagree")
        if q.answerable and not q.gold and not q.gold_refs:
            problems.append(f"{where} answerable question without gold_ranges or gold_refs")
        if q.answerable and not q.required_facts:
            problems.append(f"{where} answerable question without required_facts")
        if not q.answerable and q.gold:
            problems.append(f"{where} unanswerable question has gold ranges")
        for g in q.gold:
            if g.start < 1 or g.end < g.start:
                problems.append(f"{where} bad range {g.path}:{g.start}-{g.end}")
            if not snapshot:
                continue
            full = os.path.join(snapshot, g.path)
            if g.path not in line_counts:
                try:
                    with open(full, encoding="utf-8", errors="replace") as f:
                        line_counts[g.path] = sum(1 for _ in f)
                except OSError:
                    line_counts[g.path] = None
            n = line_counts[g.path]
            if n is None:
                problems.append(f"{where} gold path missing at pinned commit: {g.path}")
            elif g.end > n:
                problems.append(f"{where} {g.path}:{g.start}-{g.end} is past the end of the file ({n} lines)")
    return problems


def summary(es):
    by_type = collections.Counter(q.type for q in es.questions)
    by_status = collections.Counter(q.status for q in es.questions)
    later = [q.id for q in es.questions if "issues" in q.needs]
    lines = [
        f"{len(es.questions)} questions at {es.commit[:12]}",
        "  " + "  ".join(f"{t}={by_type.get(t, 0)}" for t in TYPES),
        "  status: " + "  ".join(f"{k}={v}" for k, v in sorted(by_status.items())),
    ]
    if later:
        lines.append(f"  needs issues/PRs (Milestone 3): {', '.join(later)}")
    return "\n".join(lines)


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("evalset")
    ap.add_argument("--snapshot", help="Panurus snapshot made with scripts/snapshot.sh")
    args = ap.parse_args()
    es = load(args.evalset)
    print(summary(es))
    problems = validate(es, args.snapshot)
    for p in problems:
        print("PROBLEM", p)
    if problems:
        sys.exit(1)
    print("valid" + ("" if args.snapshot else " (paths not checked: no --snapshot)"))


if __name__ == "__main__":
    main()

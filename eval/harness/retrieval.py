"""Retrieval metrics for one ablation run (PRD section 11).

    python3 eval/harness/retrieval.py eval/v1.yaml --run D-keyword --version 2 --mode keyword

Runs every question through `ask-panurus search --json`, then reports
Recall@5, Recall@10 and MRR against the gold line ranges, overall and by
question type, plus how well each refusal signal separates answerable from
unanswerable questions. Results are written to eval/reports/<run>.json.

Questions whose gold lives only in issues or PRs (gold_refs, no gold_ranges)
are listed but excluded from the recall denominators until those sources are
indexed, so the numbers are never inflated by quietly dropping misses.
"""

import argparse
import collections
import datetime
import json
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import evalset  # noqa: E402

TOP_K = 10


def search(binary, question, version, mode, tei):
    cmd = [binary, "search", question, "--json", "--top", str(TOP_K), "--mode", mode]
    if version:
        cmd += ["--version", str(version)]
    if tei:
        cmd += ["--tei", tei]
    out = subprocess.run(cmd, check=True, capture_output=True, text=True).stdout
    return json.loads(out) or []


def first_hit_rank(q, hits):
    for i, h in enumerate(hits, 1):
        if any(g.overlaps(h["path"], h["start_line"], h["end_line"]) for g in q.gold):
            return i
    return None


def covered(q, hits, k):
    """True if the top k hits satisfy the question's gold."""
    top = hits[:k]
    hit = [any(g.overlaps(h["path"], h["start_line"], h["end_line"]) for h in top) for g in q.gold]
    return all(hit) if q.require_all else any(hit)


def best_threshold(pos, neg):
    """The cut on a signal (refuse when the value is below it) that maximises
    balanced accuracy. Returns (threshold, refusal accuracy, false refusal rate)."""
    best = (None, 0.0, 1.0, -1.0)
    for t in sorted(set(pos + neg)) + [float("inf")]:
        refused = sum(1 for v in neg if v < t) / len(neg)
        false_ref = sum(1 for v in pos if v < t) / len(pos)
        score = refused - false_ref
        if score > best[3]:
            best = (t, refused, false_ref, score)
    return best[:3]


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("evalset")
    ap.add_argument("--run", required=True, help="run name, for example D-keyword")
    ap.add_argument("--version", type=int, default=0, help="index version (default: live)")
    ap.add_argument("--mode", default="hybrid", choices=["keyword", "vector", "hybrid"])
    ap.add_argument("--tei", default=os.environ.get("ASK_PANURUS_TEI", ""))
    ap.add_argument("--binary", default="./ask-panurus")
    ap.add_argument("--note", default="", help="free text stored with the run")
    args = ap.parse_args()

    es = evalset.load(args.evalset)
    rows = []
    for q in es.questions:
        hits = search(args.binary, q.question, args.version, args.mode, args.tei)
        top = hits[0] if hits else {}
        row = {
            "id": q.id,
            "type": q.type,
            "answerable": q.answerable,
            "scored": bool(q.gold),
            "top": [f"{h['path']}:{h['start_line']}-{h['end_line']}" for h in hits[:5]],
            "signals": top.get("signals") or {},
        }
        if q.gold:
            row["rank"] = first_hit_rank(q, hits)
            row["r5"] = covered(q, hits, 5)
            row["r10"] = covered(q, hits, 10)
        rows.append(row)

    scored = [r for r in rows if r["scored"]]
    pending = [r["id"] for r in rows if r["answerable"] and not r["scored"]]

    def agg(rs):
        n = len(rs)
        if not n:
            return {"n": 0}
        return {
            "n": n,
            "recall@5": sum(r["r5"] for r in rs) / n,
            "recall@10": sum(r["r10"] for r in rs) / n,
            "mrr": sum(1 / r["rank"] for r in rs if r["rank"]) / n,
            "hits@5": sum(r["r5"] for r in rs),
        }

    by_type = {t: agg([r for r in scored if r["type"] == t]) for t in evalset.TYPES if t != "unanswerable"}
    refusal = {}
    pos = [r for r in rows if r["answerable"] and r["scored"]]
    neg = [r for r in rows if not r["answerable"]]
    for sig in sorted({k for r in rows for k in r["signals"]}):
        p = [r["signals"].get(sig, 0.0) for r in pos]
        n = [r["signals"].get(sig, 0.0) for r in neg]
        t, acc, fr = best_threshold(p, n)
        refusal[sig] = {
            "threshold": t, "refusal_accuracy": acc, "false_refusal_rate": fr,
            "answerable_median": sorted(p)[len(p) // 2], "unanswerable_median": sorted(n)[len(n) // 2],
        }

    report = {
        "run": args.run,
        "date": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"),
        "evalset": args.evalset,
        "panurus_commit": es.commit,
        "index_version": args.version,
        "mode": args.mode,
        "note": args.note,
        "overall": agg(scored),
        "by_type": by_type,
        "pending_issue_sources": pending,
        "refusal_signals": refusal,
        "questions": rows,
    }
    os.makedirs("eval/reports", exist_ok=True)
    path = f"eval/reports/{args.run}.json"
    with open(path, "w") as f:
        json.dump(report, f, indent=1)

    o = report["overall"]
    print(f"run {args.run}: version {args.version or 'live'}, mode {args.mode}, {o['n']} scored questions")
    print(f"  Recall@5 {o['recall@5']:.0%} ({o['hits@5']}/{o['n']})   Recall@10 {o['recall@10']:.0%}   MRR {o['mrr']:.2f}")
    for t, a in by_type.items():
        if a["n"]:
            print(f"  {t:8} R@5 {a['recall@5']:.0%} ({a['hits@5']}/{a['n']})   MRR {a['mrr']:.2f}")
    for sig, s in refusal.items():
        print(f"  refusal on {sig:<8} < {s['threshold']:.3f}: refuses {s['refusal_accuracy']:.0%} of {len(neg)} unanswerable,"
              f" falsely refuses {s['false_refusal_rate']:.0%} of {len(pos)} answerable"
              f"  (medians {s['answerable_median']:.3f} vs {s['unanswerable_median']:.3f})")
    if pending:
        print(f"  not yet scorable (gold only in issues/PRs): {', '.join(pending)}")
    misses = [r for r in scored if not r["r5"]]
    for r in misses:
        print(f"  miss {r['id']} ({r['type']}): rank {r['rank']}; top: {r['top'][0] if r['top'] else '-'}")
    print(f"  wrote {path}")


if __name__ == "__main__":
    main()

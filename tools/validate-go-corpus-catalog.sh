#!/bin/sh
# Verify the published exclusion projection and Sprint 376 applicability overlay on the historical denominator.
set -eu

targets=docs/go-corpus-targets.tsv
exclusions=docs/go-corpus-exclusions.tsv
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT HUP INT TERM

awk -F '\t' 'NR == 1 || $5 == "excluded"' "$targets" >"$tmp"
cmp -s "$tmp" "$exclusions"

awk -F '\t' '
BEGIN { allowed["compiler-diagnostic"] = allowed["asmcheck"] = allowed["gc-only-check"] = allowed["unsafe-reinterpretation"] = allowed["gc-observation"] = allowed["cgo"] = allowed["assembly-companion"] = allowed["assembly-input"] = allowed["multi-package-interpreted"] = allowed["compute-bound"] = 1 }
NR == 1 { next }
{ if ($5 == "excluded" && !($6 in allowed)) invalid = 1; if (seen[$1 SUBSEP $4]++) invalid = 1 }
{ keys[$5]++; ids[$5 SUBSEP $1] = 1; rank = $5 == "repair" ? 4 : $5 == "review" ? 3 : $5 == "blocked-design" ? 2 : 1; if (rank > best[$1]) { best[$1] = rank; overall[$1] = $5 }; if ($5 == "excluded") families[$6] = 1 }
END {
  for (id in ids) { split(id, pair, SUBSEP); roots[pair[1]]++ }
  for (root in overall) overallRoots[overall[root]]++
  for (family in families) familyCount++
  if (invalid || keys["excluded"] != 328 || roots["excluded"] != 323 || overallRoots["excluded"] != 315 || keys["repair"] != 290 || roots["repair"] != 275 || keys["repair"] + keys["review"] + keys["blocked-design"] != 337 || overallRoots["repair"] + overallRoots["review"] + overallRoots["blocked-design"] != 316 || familyCount != 10) exit 1
}' "$targets"

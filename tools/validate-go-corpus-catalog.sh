#!/bin/sh
# Verify the published exclusion projection and Sprint 209 denominator.
set -eu

targets=docs/go-corpus-targets.tsv
exclusions=docs/go-corpus-exclusions.tsv
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT HUP INT TERM

awk -F '\t' 'NR == 1 || $5 == "excluded"' "$targets" >"$tmp"
cmp -s "$tmp" "$exclusions"

awk -F '\t' '
BEGIN { allowed["compiler-diagnostic"] = allowed["asmcheck"] = allowed["gc-only-check"] = allowed["unsafe-reinterpretation"] = allowed["gc-observation"] = allowed["cgo"] = allowed["assembly-companion"] = 1 }
NR == 1 { next }
{ if ($5 == "excluded" && !($6 in allowed)) exit 1 }
{ keys[$5]++; ids[$5 SUBSEP $1] = 1; rank = $5 == "repair" ? 4 : $5 == "review" ? 3 : $5 == "blocked-design" ? 2 : 1; if (rank > best[$1]) { best[$1] = rank; overall[$1] = $5 }; if ($5 == "excluded") families[$6] = 1 }
END {
  for (id in ids) { split(id, pair, SUBSEP); roots[pair[1]]++ }
  for (root in overall) overallRoots[overall[root]]++
  for (family in families) familyCount++
  if (keys["excluded"] != 278 || roots["excluded"] != 273 || overallRoots["excluded"] != 271 || keys["repair"] != 299 || roots["repair"] != 279 || keys["repair"] + keys["review"] + keys["blocked-design"] != 387 || overallRoots["repair"] + overallRoots["review"] + overallRoots["blocked-design"] != 360 || familyCount != 7) exit 1
}' "$targets"

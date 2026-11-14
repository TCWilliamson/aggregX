#!/usr/bin/env bash
# coverage-gate.sh <coverprofile> <min-percent> <package>...
# Fails if any listed package's statement coverage is below the minimum (ENGINEERING §2).
set -euo pipefail
profile=$1; min=$2; shift 2
fail=0
for pkg in "$@"; do
  pct=$(awk -v p="$pkg/" 'NR>1 && index($1,p)==1 { split($0,a," "); stmts+=a[2]; if (a[3]>0) cov+=a[2] }
        END { if (stmts==0) print "0"; else printf "%.1f", 100*cov/stmts }' "$profile")
  if awk -v a="$pct" -v b="$min" 'BEGIN{exit !(a<b)}'; then
    echo "FAIL $pkg coverage $pct% < $min%"; fail=1
  else
    echo "ok   $pkg coverage $pct%"
  fi
done
exit $fail

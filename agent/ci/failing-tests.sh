#!/usr/bin/env bash
# failing-tests.sh LOG: every failing top-level test of a `go test` log,
# by package, in one line ("pkg: TestA TestB; pkg2 [build failed]: …"),
# for one CI annotation (a step keeps only ten, so the first failure lines
# can hide another package's failures).
awk '
  /^--- FAIL: / { t = t " " $3 }
  /^FAIL[ \t]/ && NF >= 2 {
    s = $2
    if ($3 ~ /^\[/) s = s " " $3 " " $4
    out = out (out ? "; " : "") s ":" (t ? t : " (no test named)")
    t = ""
  }
  END {
    if (t) out = out (out ? "; " : "") "?:" t
    if (out == "") out = "none named in the log"
    print out
  }' "$1"

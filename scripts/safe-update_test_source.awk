# Test-only source transformer for portable function-level preflight tests.
# Production safe-update.sh is an executable-only privileged Bash entry point;
# tests source only this generated temporary copy. Exact marker cardinality
# makes production boundary changes fail closed instead of silently weakening
# the test harness.

BEGIN {
  in_boundary = begins = ends = 0
}

$0 == "# Direct-execution boundary begins." {
  if (in_boundary) {
    print "nested direct-execution boundary" > "/dev/stderr"
    exit 1
  }
  in_boundary = 1
  begins++
  print "# Direct-execution boundary removed from this test-only source copy."
  next
}

$0 == "# Direct-execution boundary ends." {
  if (!in_boundary) {
    print "unmatched direct-execution boundary" > "/dev/stderr"
    exit 1
  }
  in_boundary = 0
  ends++
  next
}

!in_boundary { print }

END {
  if (in_boundary || begins != 1 || ends != 1) {
    print "safe-update source-test boundary cardinality mismatch" > "/dev/stderr"
    exit 1
  }
}

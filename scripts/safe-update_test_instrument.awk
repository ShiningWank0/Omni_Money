# Test-only source transformer for the Docker-free Linux state-machine suite.
# It is never sourced or invoked by scripts/safe-update.sh. Every replacement
# has an exact cardinality check so production changes fail the test instead
# of silently creating a weak or partially instrumented updater.

BEGIN {
  path_rewrites = socket_bypasses = linux_bypasses = tool_allowances = 0
  plugin_candidates = plugin_allowances = 0
}

$0 == "PATH=\"$trusted_path\"" {
  print "instrumented_mock_bin=\"${SAFE_UPDATE_INSTRUMENTED_MOCK_BIN:?}\""
  print "PATH=\"$instrumented_mock_bin:$trusted_path\""
  path_rewrites++
  next
}

$0 == "validate_pinned_docker_socket() {" {
  print
  print "  return 0"
  socket_bypasses++
  next
}

$0 == "  if [ \"$(uname -s)\" = Linux ]; then" {
  print "  if false; then"
  linux_bypasses++
  next
}

$0 == "      *) fail \"command is outside the trusted system path: $command_name ($path)\"; return 1 ;;" {
  print "      \"$instrumented_mock_bin\"/*) ;;"
  print
  tool_allowances++
  next
}

$0 == "  for candidate in \\" {
  print
  print "    \"$instrumented_mock_bin/docker\" \\"
  plugin_candidates++
  next
}

$0 == "    case \"$canonical\" in /usr/lib/*|/usr/libexec/*|/usr/local/lib/*|/usr/local/libexec/*) ;; *) continue ;; esac" {
  print "    case \"$canonical\" in \"$instrumented_mock_bin\"/*|/usr/lib/*|/usr/libexec/*|/usr/local/lib/*|/usr/local/libexec/*) ;; *) continue ;; esac"
  plugin_allowances++
  next
}

{ print }

END {
  if (path_rewrites != 1 || socket_bypasses != 1 || linux_bypasses != 2 ||
      tool_allowances != 1 || plugin_candidates != 1 || plugin_allowances != 1) {
    print "safe-update test instrumentation cardinality mismatch" > "/dev/stderr"
    exit 1
  }
}

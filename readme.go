package main

import _ "embed"

// readme is the README, compiled in so :help can show it below the
// shortcuts, always matching this binary.
//
//go:embed README.md
var readme string

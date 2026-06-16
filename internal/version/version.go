// Package version exposes the build-time identity of the agent binary.
// Version and GitCommit are overridden at link time via -ldflags -X.
package version

import "fmt"

// Version is the source revision the binary was built from.
// Default "build" indicates an unstamped local build.
var Version = "build"

// GitCommit is the short git SHA the binary was built from.
// Default "unknown" indicates an unstamped local build.
var GitCommit = "unknown"

// String returns the human-readable identity string in the form
// "<Version> (<GitCommit>)".
func String() string {
	return fmt.Sprintf("%s (%s)", Version, GitCommit)
}

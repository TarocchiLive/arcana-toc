// Package version exposes build information for the Arcana ToC service.
package version

import "fmt"

// Build-time variables, overridable via -ldflags.
var (
	Version = "dev"
	Commit  = "unknown"
)

// String renders the build information as a single line.
func String() string {
	return fmt.Sprintf("%s (%s)", Version, Commit)
}

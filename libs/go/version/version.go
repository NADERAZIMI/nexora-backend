// Package version exposes build metadata injected at link time.
package version

var (
	// Version is the semantic version or git describe value.
	Version = "dev"
	// Commit is the git commit SHA.
	Commit = "unknown"
	// BuildDate is the RFC 3339 build timestamp.
	BuildDate = "unknown"
)

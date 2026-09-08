// Package version carries the build's identity.
//
// It exists so that "which build am I looking at?" has an answer from inside a
// running instance, rather than from whoever remembers what was deployed. The
// UI shows it in the sidebar.
package version

// Version is set at build time:
//
//	go build -ldflags="-X devops-tools/internal/version.Version=0.2.3"
//
// The Dockerfile passes it from its VERSION build argument. Left unset — a
// local `go build`, or an image built without the argument — it stays "dev",
// which is exactly what such a build should call itself.
var Version = "dev"

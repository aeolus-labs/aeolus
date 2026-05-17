package dashboard

import (
	"embed"
	"io/fs"
	"mime"
)

// daemonVersion is the version string surfaced to the dashboard UI
// (and any other /api caller) via /api/version. Defaults to "dev" for
// plain `go build`; main calls SetVersion at startup so release builds
// report the real version injected via goreleaser's -ldflags.
var daemonVersion = "dev"

// SetVersion overrides the version string returned by /api/version.
// Called once from main at startup to keep the dashboard's reported
// version in sync with the binary's --version output.
func SetVersion(v string) {
	if v != "" {
		daemonVersion = v
	}
}

//go:embed all:web
var embeddedAssets embed.FS

// assets is the React build output, rooted at web/.
// May be empty if the dashboard has not been built; in that case the
// HTTP server only exposes /api/* endpoints.
var assets fs.FS

func init() {
	// Go's default mime table doesn't know about .webmanifest; without
	// this Chrome/Edge warn ("Manifest fetched with non-JSON MIME") and
	// Safari can refuse to register the dashboard as an installable
	// app. Register both before the file server starts serving.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")

	sub, err := fs.Sub(embeddedAssets, "web")
	if err != nil {
		return
	}
	// Confirm at least one non-placeholder file exists before exposing the FS.
	hasContent := false
	_ = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if p == "README.md" {
			return nil
		}
		hasContent = true
		return fs.SkipAll
	})
	if hasContent {
		assets = sub
	}
}

package dashboard

import (
	"embed"
	"io/fs"
)

//go:embed all:web
var embeddedAssets embed.FS

// assets is the React build output, rooted at web/.
// May be empty if the dashboard has not been built; in that case the
// HTTP server only exposes /api/* endpoints.
var assets fs.FS

func init() {
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

// Package web embeds the PWA (plain HTML, CSS and JavaScript, no build step).
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// Files returns the PWA files rooted at the static directory.
func Files() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	return sub
}

// Package web embeds the built frontend (web/dist) into srvmon-server.
// Build it with `npm run build` in this directory (or `make web`); the
// Docker image does this automatically.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built frontend files.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

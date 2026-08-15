// Package web embeds the built frontend. Run `pnpm build` (or `npm run build`)
// in this directory before `go build` — the Go binary requires dist/ to exist.
package web

import "embed"

//go:embed all:dist
var Dist embed.FS

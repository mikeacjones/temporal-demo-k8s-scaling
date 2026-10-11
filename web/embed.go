// Package web holds the dashboard's static assets, embedded into the binary
// so the booth demo has no CDN or network dependencies.
package web

import "embed"

//go:embed static
var Static embed.FS

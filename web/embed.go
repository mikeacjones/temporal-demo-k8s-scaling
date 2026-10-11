// Package web holds the dashboard's static assets, embedded into the binary
// so the booth demo has no CDN or network dependencies.
package web

import "embed"

//go:embed static
var Static embed.FS

// K9s holds the booth's k9s config and skin. The in-cluster dashboard writes
// them to a scratch dir at startup (k9s needs a writable config dir).
//
//go:embed k9s
var K9s embed.FS

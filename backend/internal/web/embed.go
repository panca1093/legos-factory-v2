//go:build embedstatic

// Package web holds the production frontend build, embedded at compile time.
// Populate dist via scripts/build.sh (frontend build output copied here).
package web

import "embed"

//go:embed all:dist
var Dist embed.FS

// Enabled reports whether production assets are embedded in this build.
const Enabled = true

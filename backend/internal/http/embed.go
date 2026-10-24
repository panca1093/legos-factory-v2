//go:build embedstatic

package http

import "embed"

// webDist is the production frontend build, embedded at compile time.
// Populate dist via scripts/build.sh (frontend build output copied here).
//
//go:embed all:dist
var webDist embed.FS

// webEnabled reports whether production assets are embedded in this build.
const webEnabled = true

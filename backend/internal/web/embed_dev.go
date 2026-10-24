//go:build !embedstatic

// Package web is a no-op when building without the embedstatic tag; the
// Vite dev server (or any external static host) serves the frontend.
package web

import "embed"

// Enabled reports whether production assets are embedded in this build.
const Enabled = false

// Dist is unused in dev builds.
var Dist embed.FS

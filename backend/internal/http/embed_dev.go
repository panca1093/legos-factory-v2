//go:build !embedstatic

package http

import "embed"

// webEnabled reports whether production assets are embedded in this build.
const webEnabled = false

// webDist is empty without the embedstatic tag; the Vite dev server (or
// any external static host) serves the frontend.
var webDist embed.FS

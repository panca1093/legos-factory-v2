package httpx

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"legos-factory/backend/internal/web"
)

// mountStatic serves the embedded Vue build with SPA fallback when the
// binary was built with -tags embedstatic; otherwise it is a no-op so the
// Vite dev server can proxy API traffic instead.
func (s *Server) mountStatic() {
	if !web.Enabled {
		return
	}
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		s.log.Error("embedded frontend missing", "err", err)
		return
	}
	fileServer := http.StripPrefix("/", http.FileServer(http.FS(dist)))
	s.Echo.GET("/*", func(c echo.Context) error {
		path := c.Request().URL.Path
		if path == "/" || !strings.Contains(path[1:], ".") {
			// SPA routes and extension-less paths fall back to index.html
			data, err := fs.ReadFile(dist, "index.html")
			if err != nil {
				return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "frontend assets missing")
			}
			c.Response().Header().Set(echo.HeaderContentType, "text/html; charset=utf-8")
			return c.String(http.StatusOK, string(data))
		}
		fileServer.ServeHTTP(c.Response().Writer, c.Request())
		return nil
	})
}

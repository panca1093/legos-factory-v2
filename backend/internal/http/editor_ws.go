package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/labstack/echo/v4"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/usecase"
)

const (
	controlFrameLimit = 1 << 20
	wsWriteTimeout    = 30 * time.Second
)

// editorLiveRoutes registers the editor control WebSocket. It is gated like every editor route: dev auth mode,
// Engineer, loopback Host.
func (s *Server) editorLiveRoutes(g *echo.Group) {
	g.GET("/editor/folders/:fid/ws", s.editorLive(s.handleEditorControlWS))
}

func (s *Server) editorLive(h echo.HandlerFunc) echo.HandlerFunc {
	return s.local(func(c echo.Context) error {
		if s.EditorLive == nil {
			return errorJSON(c, http.StatusNotFound, "NOT_AVAILABLE", "the editor is not available")
		}
		return h(c)
	})
}

// editorOrigin reports whether the request's Origin is the frontend or this server itself.
func (s *Server) editorOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	return sameOrigin(o, s.Cfg.FrontendOrigin) || sameOrigin(o, "http://"+r.Host) || sameOrigin(o, "https://"+r.Host)
}

func sameOrigin(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	if errA != nil || errB != nil || ua.Host == "" || ub.Host == "" {
		return false
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Host, ub.Host)
}

// foreignOrigin answers 403 and reports true when the request carries an Origin that is not allowed.
func (s *Server) foreignOrigin(c echo.Context) bool {
	if o := c.Request().Header.Get("Origin"); o != "" && !s.editorOrigin(c.Request()) {
		_ = errorJSON(c, http.StatusForbidden, "FORBIDDEN_ORIGIN", "this origin may not call the editor")
		return true
	}
	return false
}

// preUpgrade runs every check that can still answer with a plain HTTP status.
func (s *Server) preUpgrade(c echo.Context) (model.EditorFolder, error) {
	r := c.Request()
	if !s.editorOrigin(r) {
		return model.EditorFolder{}, errorJSON(c, http.StatusForbidden, "FORBIDDEN_ORIGIN", "WebSocket Origin is missing or not allowed")
	}
	f, err := s.EditorLive.OwnedFolder(r.Context(), currentUser(c), c.Param("fid"))
	switch {
	case errors.Is(err, model.ErrEditorFolderNotFound), usecase.IsNotFound(err):
		return f, errorJSON(c, http.StatusNotFound, "NOT_FOUND", "folder not found")
	case errors.Is(err, model.ErrFolderGone):
		return f, errorJSON(c, http.StatusGone, "FOLDER_GONE", "this folder no longer exists")
	case err != nil:
		return f, errorJSON(c, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
	return f, nil
}

func (s *Server) acceptEditorWS(c echo.Context) (*websocket.Conn, error) {
	r := c.Request()
	patterns := []string{r.Host}
	if u, err := url.Parse(s.Cfg.FrontendOrigin); err == nil && u.Host != "" {
		patterns = append(patterns, u.Host)
	}
	return websocket.Accept(c.Response(), r, &websocket.AcceptOptions{OriginPatterns: patterns})
}

type controlIn struct {
	Type  string   `json:"type"`
	Dirs  []string `json:"dirs"`
	Files []string `json:"files"`
}

func (s *Server) handleEditorControlWS(c echo.Context) error {
	f, err := s.preUpgrade(c)
	if err != nil || c.Response().Committed {
		return err
	}
	user := currentUser(c)
	conn, err := s.acceptEditorWS(c)
	if err != nil {
		return nil
	}
	defer conn.CloseNow()
	conn.SetReadLimit(controlFrameLimit)
	sess, err := s.EditorLive.OpenControl(c.Request().Context(), user, f.ID)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "cannot watch this folder")
		return nil
	}
	defer sess.Close()

	ctx, cancel := context.WithCancel(c.Request().Context())
	defer cancel()
	go func() {
		defer cancel()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var in controlIn
			if typ != websocket.MessageText || json.Unmarshal(data, &in) != nil {
				continue
			}
			if in.Type == "watch" {
				_ = sess.Watch(in.Dirs, in.Files)
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return nil
		case ev := <-sess.Events():
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			wctx, wc := context.WithTimeout(ctx, wsWriteTimeout)
			err = conn.Write(wctx, websocket.MessageText, b)
			wc()
			if err != nil {
				return nil
			}
			if _, gone := ev.(usecase.FolderGoneMsg); gone {
				_ = conn.Close(websocket.StatusNormalClosure, "folder gone")
				return nil
			}
		}
	}
}

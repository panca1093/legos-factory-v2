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
	lspFrameLimit     = 16 << 20
	controlFrameLimit = 1 << 20
	wsWriteTimeout    = 30 * time.Second
)

// editorLiveRoutes registers the language server routes and both editor WebSockets. They are gated like every
// editor route: dev auth mode, Engineer, loopback Host.
func (s *Server) editorLiveRoutes(g *echo.Group) {
	g.GET("/editor/servers", s.editorLive(s.handleEditorServers))
	g.POST("/editor/servers/check", s.editorLive(s.handleEditorServersCheck))
	g.PUT("/editor/servers/:id", s.editorLive(s.handleEditorServerPath))
	g.GET("/editor/folders/:fid/ws", s.editorLive(s.handleEditorControlWS))
	g.GET("/editor/folders/:fid/lsp/:server", s.editorLive(s.handleEditorLSPWS))
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

func (s *Server) handleEditorServers(c echo.Context) error {
	states, err := s.EditorLive.ServerStates(c.Request().Context(), currentUser(c).ID)
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
	return c.JSON(http.StatusOK, states)
}

func (s *Server) handleEditorServersCheck(c echo.Context) error {
	if s.foreignOrigin(c) {
		return nil
	}
	states, err := s.EditorLive.CheckServers(c.Request().Context(), currentUser(c).ID)
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
	return c.JSON(http.StatusOK, states)
}

func (s *Server) handleEditorServerPath(c echo.Context) error {
	if s.foreignOrigin(c) {
		return nil
	}
	if !strings.HasPrefix(c.Request().Header.Get(echo.HeaderContentType), echo.MIMEApplicationJSON) {
		return errorJSON(c, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "send application/json")
	}
	var req struct {
		Path    string `json:"path"`
		Confirm bool   `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	st, err := s.EditorLive.SetServerPath(c.Request().Context(), currentUser(c).ID, c.Param("id"), req.Path, req.Confirm)
	if errors.Is(err, usecase.ErrInvalid) {
		return errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
	}
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
	return c.JSON(http.StatusOK, st)
}

// preUpgrade runs every check that can still answer with a plain HTTP status.
func (s *Server) preUpgrade(c echo.Context) (model.EditorFolder, error) {
	r := c.Request()
	if !s.editorOrigin(r) {
		return model.EditorFolder{}, errorJSON(c, http.StatusForbidden, "FORBIDDEN_ORIGIN", "WebSocket Origin is missing or not allowed")
	}
	f, err := s.EditorLive.OwnedFolder(r.Context(), currentUser(c).ID, c.Param("fid"))
	switch {
	case errors.Is(err, usecase.ErrEditorFolderNotFound):
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
	sess, err := s.EditorLive.OpenControl(c.Request().Context(), user.ID, f.ID)
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

func lspCloseFor(err error) (int, string) {
	var crashed *usecase.LSPCrashedError
	switch {
	case errors.Is(err, model.ErrUntrusted):
		return usecase.CloseUntrusted, "folder is not trusted"
	case errors.Is(err, usecase.ErrLSPUnknown), errors.Is(err, usecase.ErrLSPNotFound):
		return usecase.CloseNotFound, "language server not found"
	case errors.Is(err, model.ErrTooManyLSP):
		return usecase.CloseTooMany, "too many language servers running"
	case errors.As(err, &crashed):
		return usecase.CloseCrashed, "language server crashed"
	case errors.Is(err, model.ErrFolderGone):
		return int(websocket.StatusGoingAway), "folder gone"
	}
	return int(websocket.StatusInternalError), "cannot start the language server"
}

func (s *Server) handleEditorLSPWS(c echo.Context) error {
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
	conn.SetReadLimit(lspFrameLimit)
	client, err := s.EditorLive.OpenLSP(c.Request().Context(), user.ID, f.ID, c.Param("server"))
	if err != nil {
		code, reason := lspCloseFor(err)
		_ = conn.Close(websocket.StatusCode(code), reason)
		return nil
	}
	defer client.Close()

	ctx, cancel := context.WithCancel(c.Request().Context())
	defer cancel()
	go func() {
		defer cancel()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				_ = conn.Close(websocket.StatusUnsupportedData, "text frames only")
				return
			}
			if err := client.Send(data); err != nil {
				if !errors.Is(err, usecase.ErrLSPClosed) {
					_ = conn.Close(websocket.StatusInvalidFramePayloadData, "invalid JSON-RPC message")
				}
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-client.Done():
			code := client.CloseCode()
			if code == 0 {
				code = int(websocket.StatusNormalClosure)
			}
			_ = conn.Close(websocket.StatusCode(code), closeReason(code))
			return nil
		case msg := <-client.Messages():
			wctx, wc := context.WithTimeout(ctx, wsWriteTimeout)
			err := conn.Write(wctx, websocket.MessageText, msg)
			wc()
			if err != nil {
				return nil
			}
		}
	}
}

func closeReason(code int) string {
	switch code {
	case usecase.CloseTakenOver:
		return "Opened in another tab"
	case usecase.CloseCrashed:
		return "language server crashed"
	case usecase.CloseSlowClient:
		return "client too slow"
	}
	return ""
}

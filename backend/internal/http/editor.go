package http

import (
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/usecase"
)

// editorRoutes registers the editor's REST routes. Every one is behind Server.local, so they
// answer 404 unless the backend runs in dev auth mode on the user's machine.
func (s *Server) editorRoutes(g *echo.Group) {
	g.GET("/projects/:id/editor", s.edit(s.handleEditorProject, false))
	g.POST("/editor/folders", s.edit(s.handleEditorWorktree, true))
	g.POST("/editor/folders/:fid/trust", s.edit(s.handleEditorTrust, true))
	g.GET("/editor/folders/:fid", s.edit(s.handleEditorFolder, false))
	g.GET("/editor/folders/:fid/tree", s.edit(s.handleEditorTree, false))
	g.GET("/editor/folders/:fid/files", s.edit(s.handleEditorFiles, false))
	g.GET("/editor/folders/:fid/file", s.edit(s.handleEditorRead, false))
	g.PUT("/editor/folders/:fid/file", s.edit(s.handleEditorWrite, true), echomw.BodyLimit("12MiB"))
	g.POST("/editor/folders/:fid/mkdir", s.edit(s.handleEditorMkdir, true))
	g.POST("/editor/folders/:fid/rename", s.edit(s.handleEditorRename, true))
}

// edit gates an editor handler: local-only, an allowed Origin when one is sent, and JSON bodies
// on mutating routes so a cross-site form post cannot reach them.
func (s *Server) edit(h echo.HandlerFunc, mutating bool) echo.HandlerFunc {
	return s.local(func(c echo.Context) error {
		if s.Editor == nil {
			return errorJSON(c, http.StatusNotFound, "NOT_AVAILABLE", "the editor is not available")
		}
		if !s.originAllowed(c) {
			return errorJSON(c, http.StatusForbidden, "FORBIDDEN_ORIGIN", "this origin may not use the editor")
		}
		if mutating {
			if mt, _, err := mime.ParseMediaType(c.Request().Header.Get(echo.HeaderContentType)); err != nil || mt != echo.MIMEApplicationJSON {
				return errorJSON(c, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/json")
			}
		}
		return h(c)
	})
}

// originAllowed accepts a request without an Origin header, or one from the configured frontend
// or from this server itself.
func (s *Server) originAllowed(c echo.Context) bool {
	o := c.Request().Header.Get(echo.HeaderOrigin)
	if o == "" {
		return true
	}
	o = strings.TrimRight(o, "/")
	return o == strings.TrimRight(s.Cfg.FrontendOrigin, "/") || o == c.Scheme()+"://"+c.Request().Host
}

func editorError(c echo.Context, err error) error {
	var wc *usecase.WriteConflict
	var ec *model.EditorConflict
	switch {
	case errors.As(err, &wc):
		return editorConflict(c, wc.Current, wc.Actor)
	case errors.As(err, &ec):
		return editorConflict(c, ec.Current, nil)
	case errors.Is(err, model.ErrOutsideFolder):
		return errorJSON(c, http.StatusForbidden, "OUTSIDE_FOLDER", err.Error())
	case errors.Is(err, model.ErrTooLarge):
		return sizedError(c, http.StatusRequestEntityTooLarge, "TOO_LARGE", err)
	case errors.Is(err, model.ErrBinary):
		return sizedError(c, http.StatusUnsupportedMediaType, "BINARY", err)
	case errors.Is(err, model.ErrFolderGone):
		return errorJSON(c, http.StatusNotFound, "FOLDER_GONE", err.Error())
	case errors.Is(err, model.ErrUntrusted):
		return errorJSON(c, http.StatusForbidden, "UNTRUSTED", err.Error())
	case errors.Is(err, model.ErrTooManyLSP):
		return errorJSON(c, http.StatusTooManyRequests, "TOO_MANY_SERVERS", err.Error())
	case errors.Is(err, model.ErrBadPath), errors.Is(err, usecase.ErrInvalid):
		return errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
	case errors.Is(err, fs.ErrExist):
		return errorJSON(c, http.StatusConflict, "CONFLICT", "something already exists at that path")
	case errors.Is(err, model.ErrEditorFolderNotFound), usecase.IsNotFound(err), errors.Is(err, fs.ErrNotExist):
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "not found")
	}
	return errorJSON(c, http.StatusInternalServerError, "INTERNAL", err.Error())
}

func sizedError(c echo.Context, status int, code string, err error) error {
	rid := c.Response().Header().Get(echo.HeaderXRequestID)
	var size int64
	var se *model.SizedError
	if errors.As(err, &se) {
		size = se.Size
	}
	return c.JSON(status, map[string]any{
		"error": model.APIError{Code: code, Message: err.Error(), RequestID: rid}, "request_id": rid, "size": size,
	})
}

func (s *Server) handleEditorFolder(c echo.Context) error {
	f, err := s.Editor.Open(c.Request().Context(), currentUser(c), c.Param("fid"))
	if err != nil {
		return editorError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"folder": f})
}

func editorConflict(c echo.Context, cur model.FileStat, actor *model.EditorActor) error {
	rid := c.Response().Header().Get(echo.HeaderXRequestID)
	return c.JSON(http.StatusConflict, map[string]any{
		"error":        model.APIError{Code: "CONFLICT", Message: "file changed on disk", RequestID: rid},
		"request_id":   rid,
		"current_hash": cur.Hash, "mtime": cur.MTime, "size": cur.Size, "actor": actor,
	})
}

func bindEditor(c echo.Context, req any) error {
	if err := c.Bind(req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	return nil
}

func (s *Server) handleEditorProject(c echo.Context) error {
	f, err := s.Editor.ProjectFolder(c.Request().Context(), currentUser(c), c.Param("id"))
	if err != nil {
		return editorError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"folder": f})
}

func (s *Server) handleEditorWorktree(c echo.Context) error {
	var req struct {
		WOID   string `json:"wo_id"`
		TaskID string `json:"task_id"`
	}
	if err := bindEditor(c, &req); err != nil {
		return err
	}
	f, err := s.Editor.WorktreeFolder(c.Request().Context(), currentUser(c), req.WOID, req.TaskID)
	if err != nil {
		return editorError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"folder": f})
}

func (s *Server) handleEditorTrust(c echo.Context) error {
	var req struct {
		Trusted bool `json:"trusted"`
	}
	if err := bindEditor(c, &req); err != nil {
		return err
	}
	f, err := s.Editor.SetTrusted(c.Request().Context(), currentUser(c), c.Param("fid"), req.Trusted)
	if err != nil {
		return editorError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"folder": f})
}

func (s *Server) handleEditorTree(c echo.Context) error {
	entries, truncated, err := s.Editor.Tree(c.Request().Context(), currentUser(c), c.Param("fid"), c.QueryParam("path"))
	if err != nil {
		return editorError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"entries": entries, "truncated": truncated})
}

func (s *Server) handleEditorFiles(c echo.Context) error {
	paths, err := s.Editor.Files(c.Request().Context(), currentUser(c), c.Param("fid"))
	if err != nil {
		return editorError(c, err)
	}
	return c.JSON(http.StatusOK, paths)
}

func (s *Server) handleEditorRead(c echo.Context) error {
	fc, err := s.Editor.Read(c.Request().Context(), currentUser(c), c.Param("fid"), c.QueryParam("path"))
	if err != nil {
		return editorError(c, err)
	}
	return c.JSON(http.StatusOK, fc)
}

func (s *Server) handleEditorWrite(c echo.Context) error {
	var req struct {
		Path     string `json:"path"`
		Content  string `json:"content"`
		BaseHash string `json:"base_hash"`
		Create   bool   `json:"create"`
	}
	if err := bindEditor(c, &req); err != nil {
		return err
	}
	st, err := s.Editor.Write(c.Request().Context(), currentUser(c), c.Param("fid"), req.Path, req.Content, req.BaseHash, req.Create)
	if err != nil {
		return editorError(c, err)
	}
	if s.EditorLive != nil {
		s.EditorLive.NoteWrite(currentUser(c).ID, c.Param("fid"), path.Clean(req.Path), st.Hash)
	}
	return c.JSON(http.StatusOK, st)
}

func (s *Server) handleEditorMkdir(c echo.Context) error {
	var req struct {
		Path string `json:"path"`
	}
	if err := bindEditor(c, &req); err != nil {
		return err
	}
	if err := s.Editor.Mkdir(c.Request().Context(), currentUser(c), c.Param("fid"), req.Path); err != nil {
		return editorError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]string{"path": req.Path})
}

func (s *Server) handleEditorRename(c echo.Context) error {
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := bindEditor(c, &req); err != nil {
		return err
	}
	if err := s.Editor.Rename(c.Request().Context(), currentUser(c), c.Param("fid"), req.From, req.To); err != nil {
		return editorError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]string{"from": req.From, "to": req.To})
}

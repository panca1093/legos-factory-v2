package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"legos-factory/backend/internal/usecase"
)

const chatFrameTimeout = 10 * time.Second

func chatError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, usecase.ErrChatBusy):
		return errorJSON(c, http.StatusConflict, "CHAT_BUSY", err.Error())
	case errors.Is(err, usecase.ErrChatSaturated):
		return errorJSON(c, http.StatusTooManyRequests, "CHAT_BUSY", err.Error())
	case errors.Is(err, usecase.ErrChatBudget):
		return errorJSON(c, http.StatusConflict, "BUDGET_EXHAUSTED", err.Error())
	case errors.Is(err, usecase.ErrDraftFailed):
		return errorJSON(c, http.StatusBadGateway, "DRAFT_FAILED", err.Error())
	case errors.Is(err, usecase.ErrChatUnavailable):
		return errorJSON(c, http.StatusServiceUnavailable, "CHAT_UNAVAILABLE", err.Error())
	}
	return mapDomainError(c, err)
}

func (s *Server) handleListChats(c echo.Context) error {
	chats, err := s.Chat.List(c.Request().Context(), currentUser(c))
	if err != nil {
		return chatError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"chats": chats})
}

func (s *Server) handleCreateChat(c echo.Context) error {
	var req struct {
		Title     string `json:"title"`
		ProjectID string `json:"project_id"`
		Model     string `json:"model"`
	}
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	ch, err := s.Chat.Create(c.Request().Context(), currentUser(c), req.Title, req.ProjectID, req.Model)
	if err != nil {
		return chatError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]any{"chat": ch})
}

func (s *Server) handleGetChat(c echo.Context) error {
	ch, err := s.Chat.Get(c.Request().Context(), currentUser(c), c.Param("id"))
	if err != nil {
		return chatError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"chat": ch})
}

func (s *Server) handleUpdateChat(c echo.Context) error {
	var req struct {
		Title     *string `json:"title"`
		Model     *string `json:"model"`
		ProjectID *string `json:"project_id"`
	}
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	ch, err := s.Chat.Update(c.Request().Context(), currentUser(c), c.Param("id"), req.Title, req.Model, req.ProjectID)
	if err != nil {
		return chatError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"chat": ch})
}

func (s *Server) handleDeleteChat(c echo.Context) error {
	if err := s.Chat.Delete(c.Request().Context(), currentUser(c), c.Param("id")); err != nil {
		return chatError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) handleChatMessages(c echo.Context) error {
	msgs, err := s.Chat.Messages(c.Request().Context(), currentUser(c), c.Param("id"))
	if err != nil {
		return chatError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"messages": msgs})
}

func (s *Server) handleChatSession(c echo.Context) error {
	ses, err := s.Chat.Session(c.Request().Context(), currentUser(c), c.Param("id"))
	if err != nil {
		return chatError(c, err)
	}
	return c.JSON(http.StatusOK, ses)
}

func (s *Server) handleChatWorkOrderDraft(c echo.Context) error {
	d, err := s.Chat.DraftWorkOrder(c.Request().Context(), currentUser(c), c.Param("id"))
	if err != nil {
		return chatError(c, err)
	}
	return c.JSON(http.StatusOK, d)
}

func (s *Server) handleChatRelatedKnowledge(c echo.Context) error {
	docs, err := s.Chat.RelatedKnowledge(c.Request().Context(), currentUser(c), c.Param("id"))
	if err != nil {
		return chatError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"docs": docs})
}

func (s *Server) handleChatModels(c echo.Context) error {
	list, err := s.Chat.ModelList(usecase.WithChatUser(c.Request().Context(), currentUser(c).ID))
	if err != nil {
		return errorJSON(c, http.StatusBadGateway, "MODELS_UNAVAILABLE", err.Error())
	}
	return c.JSON(http.StatusOK, list)
}

func (s *Server) handleChatPermission(c echo.Context) error {
	var req struct {
		Decision string `json:"decision"`
	}
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	if err := s.Chat.AnswerPermission(c.Request().Context(), currentUser(c), c.Param("id"), c.Param("reqId"), req.Decision); err != nil {
		return chatError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) handleChatFileUpload(c echo.Context) error {
	mr, err := c.Request().MultipartReader()
	if err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "multipart upload required")
	}
	for {
		part, err := mr.NextPart()
		if err != nil {
			var he *echo.HTTPError
			if errors.As(err, &he) {
				return he
			}
			return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "multipart field \"file\" is required")
		}
		if part.FormName() != "file" || part.FileName() == "" {
			continue
		}
		rec, err := s.Chat.Upload(c.Request().Context(), currentUser(c), c.Param("id"), part.FileName(), part)
		var he *echo.HTTPError
		if errors.As(err, &he) {
			return he
		}
		if err != nil {
			return chatError(c, err)
		}
		return c.JSON(http.StatusCreated, rec)
	}
}

func (s *Server) handleChatFileGet(c echo.Context) error {
	rec, f, err := s.Chat.OpenFile(c.Request().Context(), currentUser(c), c.Param("id"), c.Param("fileId"))
	if err != nil {
		return chatError(c, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return chatError(c, err)
	}
	h := c.Response().Header()
	h.Set(echo.HeaderContentType, rec.MediaType)
	h.Set("X-Content-Type-Options", "nosniff")
	if rec.Kind != "pdf" { // Chrome's PDF viewer refuses to render a sandboxed document
		h.Set("Content-Security-Policy", "sandbox")
	}
	h.Set("Content-Disposition", "inline; filename*=UTF-8''"+attrEscape(rec.Name))
	h.Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(c.Response(), c.Request(), "", info.ModTime(), f)
	return nil
}

func (s *Server) handleChatFileDelete(c echo.Context) error {
	if err := s.Chat.RemoveFile(c.Request().Context(), currentUser(c), c.Param("id"), c.Param("fileId")); err != nil {
		return chatError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// attrEscape percent-encodes s for an RFC 5987 ext-value.
func attrEscape(s string) string {
	const attrChars = "!#$&+-.^_`|~"
	var b strings.Builder
	for _, by := range []byte(s) {
		switch {
		case by >= 'a' && by <= 'z', by >= 'A' && by <= 'Z', by >= '0' && by <= '9', strings.IndexByte(attrChars, by) >= 0:
			b.WriteByte(by)
		default:
			fmt.Fprintf(&b, "%%%02X", by)
		}
	}
	return b.String()
}

// chatStepTool is model.AgentTool with every field always present in a step frame.
type chatStepTool struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Failed bool   `json:"failed"`
}

// handleChatSend answers the message as a server-sent event stream:
// user_message, repo (a project chat), delta, step, permission and permission_resolved..., then done or error.
// Failures before the message is stored are ordinary JSON errors.
func (s *Server) handleChatSend(c echo.Context) error {
	var in usecase.ChatSend
	if err := c.Bind(&in); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	res := c.Response()
	flusher, _ := res.Writer.(http.Flusher)
	rc := http.NewResponseController(res.Writer)
	started, dead := false, false
	emit := func(ev usecase.ChatEvent) {
		if dead {
			return
		}
		// a stalled client must not hold the agent's reader or the chat's busy flag
		_ = rc.SetWriteDeadline(time.Now().Add(chatFrameTimeout))
		if !started {
			started = true
			res.Header().Set(echo.HeaderContentType, "text/event-stream")
			res.Header().Set("Cache-Control", "no-cache")
			res.Header().Set("X-Accel-Buffering", "no")
			res.WriteHeader(http.StatusOK)
		}
		payload := map[string]any{}
		switch ev.Kind {
		case "delta":
			payload["text"] = ev.Text
		case "title":
			payload["title"] = ev.Text
		case "repo":
			payload["project_id"], payload["sha"] = ev.ProjectID, ev.SHA
		case "step":
			tools := make([]chatStepTool, 0, len(ev.Tools))
			for _, t := range ev.Tools {
				tools = append(tools, chatStepTool(t))
			}
			payload["tools"] = tools
		case "permission":
			payload["id"], payload["server"], payload["tool"], payload["input"] = ev.ReqID, ev.Server, ev.Tool, ev.Input
		case "permission_resolved":
			payload["id"], payload["outcome"] = ev.ReqID, ev.Outcome
		case "error":
			payload["message"], payload["error"] = ev.Message, ev.Error
		default:
			payload["message"] = ev.Message
		}
		b, _ := json.Marshal(payload)
		if _, err := fmt.Fprintf(res.Writer, "event: %s\ndata: %s\n\n", ev.Kind, b); err != nil {
			dead = true
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	if err := s.Chat.Send(c.Request().Context(), currentUser(c), c.Param("id"), in, emit); err != nil {
		return chatError(c, err)
	}
	return nil
}

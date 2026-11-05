package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"

	"legos-factory/backend/internal/usecase"
)

func chatError(c echo.Context, err error) error {
	if errors.Is(err, usecase.ErrChatBusy) {
		return errorJSON(c, http.StatusConflict, "CHAT_BUSY", err.Error())
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
		Title *string `json:"title"`
		Model *string `json:"model"`
	}
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	ch, err := s.Chat.Update(c.Request().Context(), currentUser(c), c.Param("id"), req.Title, req.Model)
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

func (s *Server) handleChatModels(c echo.Context) error {
	list, err := s.Chat.ModelList(c.Request().Context())
	if err != nil {
		return errorJSON(c, http.StatusBadGateway, "MODELS_UNAVAILABLE", err.Error())
	}
	return c.JSON(http.StatusOK, list)
}

// handleChatSend answers the message as a server-sent event stream:
// user_message, delta..., then done or error. Failures before the message is
// stored are ordinary JSON errors.
func (s *Server) handleChatSend(c echo.Context) error {
	var in usecase.ChatSend
	if err := c.Bind(&in); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	res := c.Response()
	flusher, _ := res.Writer.(http.Flusher)
	started := false
	emit := func(ev usecase.ChatEvent) {
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
		case "error":
			payload["message"], payload["error"] = ev.Message, ev.Error
		default:
			payload["message"] = ev.Message
		}
		b, _ := json.Marshal(payload)
		fmt.Fprintf(res.Writer, "event: %s\ndata: %s\n\n", ev.Kind, b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	if err := s.Chat.Send(c.Request().Context(), currentUser(c), c.Param("id"), in, emit); err != nil {
		return chatError(c, err)
	}
	return nil
}

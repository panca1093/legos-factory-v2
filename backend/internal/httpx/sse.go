package httpx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"legos-factory/backend/internal/model"
)

// SSE streams. Events are both replayed from the persistent log (after
// Last-Event-ID) and pushed live from the event bus.

func (s *Server) handleWOEvents(c echo.Context) error {
	woID := c.Param("id")
	if _, err := s.takeWO(c); err != nil {
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "work order not found")
	}
	return s.stream(c, woID)
}

func (s *Server) handleGlobalEvents(c echo.Context) error {
	return s.stream(c, "")
}

func (s *Server) stream(c echo.Context, woID string) error {
	logKey := woID
	if logKey == "" {
		logKey = "*"
	}
	lastID := int64(0)
	if v := c.Request().Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			lastID = n
		}
	}
	if q := c.QueryParam("after"); q != "" {
		if n, err := strconv.ParseInt(q, 10, 64); err == nil && n > lastID {
			lastID = n
		}
	}

	res := c.Response()
	res.Header().Set(echo.HeaderContentType, "text/event-stream")
	res.Header().Set("Cache-Control", "no-cache")
	res.Header().Set("X-Accel-Buffering", "no")
	res.WriteHeader(http.StatusOK)

	sub, unsub := s.Bus.Subscribe(logKey)
	defer unsub()

	flusher, ok := res.Writer.(http.Flusher)
	if !ok {
		return nil
	}

	ctx := c.Request().Context()
	// replay persisted events after lastID
	if woID != "" {
		events, err := s.WORepo.Events(ctx, woID, lastID, 500)
		if err == nil {
			for _, ev := range events {
				fmt.Fprintf(res.Writer, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Kind, s.eventJSON(ev))
			}
			flusher.Flush()
		}
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-heartbeat.C:
			if _, err := fmt.Fprint(res.Writer, ": ping\n\n"); err != nil {
				return nil
			}
			flusher.Flush()
		case ev, open := <-sub:
			if !open {
				return nil
			}
			fmt.Fprintf(res.Writer, "event: %s\ndata: %s\n\n", ev.Kind, s.liveJSON(woID, ev))
			flusher.Flush()
		}
	}
}

// eventJSON serializes a persisted event; liveJSON a bus-delivered one.
func (s *Server) eventJSON(ev model.Event) string {
	b, _ := json.Marshal(ev)
	return string(b)
}

func (s *Server) liveJSON(woID string, ev model.LiveEvent) string {
	seq, _ := ev.Payload["id"].(float64)
	b, _ := json.Marshal(map[string]any{
		"id":            int64(seq),
		"work_order_id": firstNonEmpty(woID, ev.WOID),
		"actor":         ev.Actor,
		"kind":          ev.Kind,
		"message":       ev.Message,
		"payload":       ev.Payload["payload"],
	})
	return string(b)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

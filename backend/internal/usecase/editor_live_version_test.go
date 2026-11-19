package usecase

import (
	"context"
	"encoding/json"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestServerStateEventsKeepTheVersion(t *testing.T) {
	l, _, _, _ := newLive(t, true)
	s, _ := l.OpenControl(context.Background(), usr("u1"), "f1")
	defer s.Close()
	nextEvent(t, s, func(m ServerStateMsg) bool { return m.Server == "gopls" })
	l.Reg.notify(LSPKey{User: "u1", Folder: "f1", Server: "gopls"}, LSPStateEvent{Server: "gopls", State: model.LSPRunning})
	m := nextEvent(t, s, func(m ServerStateMsg) bool { return m.Server == "gopls" && m.State == model.LSPRunning })
	if m.Version != "1.0.0" {
		t.Fatalf("%+v", m)
	}
	p := 40
	b, _ := json.Marshal(ServerStateMsg{Type: "server_state", Server: "gopls", State: "running", Progress: &p})
	if string(b) != `{"type":"server_state","server":"gopls","state":"running","progress":40}` {
		t.Fatalf("%s", b)
	}
}

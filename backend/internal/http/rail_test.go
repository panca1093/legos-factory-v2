package http

import (
	"encoding/json"
	"net/http"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/usecase"
)

func TestProjectRailEndpoint(t *testing.T) {
	srv, wos, data := bootDelete(t)
	srv.Rail = &usecase.Rail{Projects: srv.Projects, WO: wos}
	mkWO(t, wos, data, "wo_run", "p1", model.StateImplementing)
	mkWO(t, wos, data, "wo_ask", "p1", model.StateWaitingForHuman)
	mkWO(t, wos, data, "wo_done", "p1", model.StateCancelled)

	if code, _ := chatDo(srv, "", "GET", "/api/v1/projects/p1/rail", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	if code, _ := chatDo(srv, "bob", "GET", "/api/v1/projects/p1/rail", ""); code != http.StatusNotFound {
		t.Fatalf("non-member: %d", code)
	}
	if code, _ := chatDo(srv, "ana", "GET", "/api/v1/projects/nope/rail", ""); code != http.StatusNotFound {
		t.Fatalf("unknown project: %d", code)
	}
	if code, _ := chatDo(srv, "ana", "GET", "/api/v1/projects/p1/rail?part=x", ""); code != http.StatusBadRequest {
		t.Fatalf("bad part: %d", code)
	}

	code, body := chatDo(srv, "ana", "GET", "/api/v1/projects/p1/rail", "")
	if code != http.StatusOK {
		t.Fatalf("member: %d %s", code, body)
	}
	var res struct {
		Project    struct{ ID, Name, Repo string }
		WorkOrders struct {
			Open int
			Rows []struct {
				ID           string
				WaitingOnYou bool `json:"waiting_on_you"`
			}
		} `json:"work_orders"`
		Pulls struct {
			Identity *struct{}
			Count    int
			Rows     []any
			Error    string
		}
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatal(err)
	}
	if res.WorkOrders.Open != 2 || len(res.WorkOrders.Rows) != 2 || res.WorkOrders.Rows[0].ID != "wo_ask" || !res.WorkOrders.Rows[0].WaitingOnYou {
		t.Fatalf("work orders: %s", body)
	}
	if res.Pulls.Error != "no_identity" || res.Pulls.Count != 0 || res.Pulls.Identity != nil || res.Pulls.Rows == nil {
		t.Fatalf("pulls: %s", body)
	}
	if res.Project.ID != "p1" {
		t.Fatalf("project: %s", body)
	}

	code, body = chatDo(srv, "ana", "GET", "/api/v1/projects/p1/rail?part=pulls", "")
	var pulls map[string]json.RawMessage
	_ = json.Unmarshal([]byte(body), &pulls)
	if code != http.StatusOK || pulls["work_orders"] != nil || pulls["pulls"] == nil {
		t.Fatalf("part=pulls: %d %s", code, body)
	}
}

package model

import "time"

type RailProject struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Repo     string `json:"repo"`
	NeedsYou int    `json:"needs_you"`
}

type RailWorkOrder struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	State        State     `json:"state"`
	Stage        string    `json:"stage"`
	WaitingOnYou bool      `json:"waiting_on_you"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type RailWorkOrders struct {
	Open int             `json:"open"`
	Rows []RailWorkOrder `json:"rows"`
}

// RailPulls is a stub until the Review PR backend lands: rows are always empty.
type RailPulls struct {
	Identity any        `json:"identity"`
	Count    int        `json:"count"`
	Rows     []struct{} `json:"rows"`
	Stale    bool       `json:"stale"`
	Error    string     `json:"error,omitempty"`
}

// RailResponse parts are pointers so a partial request omits the others.
type RailResponse struct {
	Project    RailProject     `json:"project"`
	WorkOrders *RailWorkOrders `json:"work_orders,omitempty"`
	Pulls      *RailPulls      `json:"pulls,omitempty"`
}

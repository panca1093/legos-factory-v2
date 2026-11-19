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

type RailPullRow struct {
	Number        int    `json:"number"`
	Title         string `json:"title"`
	Label         string `json:"label"`
	PendingDrafts int    `json:"pending_drafts"`
	Running       bool   `json:"running"`
}

type RailPulls struct {
	Identity *IdentityView `json:"identity"`
	Count    int           `json:"count"`
	Rows     []RailPullRow `json:"rows"`
	Stale    bool          `json:"stale"`
	Error    *ProviderErr  `json:"error,omitempty"`
}

// RailResponse parts are pointers so a partial request omits the others.
type RailResponse struct {
	Project    RailProject     `json:"project"`
	WorkOrders *RailWorkOrders `json:"work_orders,omitempty"`
	Pulls      *RailPulls      `json:"pulls,omitempty"`
}

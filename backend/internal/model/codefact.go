package model

// CodeFact is one verified claim about the code, bound to the commit it was checked at.
type CodeFact struct {
	Path  string `json:"path"`
	Line  int    `json:"line,omitempty"`
	Claim string `json:"claim"`
	By    string `json:"by"`
	SHA   string `json:"sha"`
}

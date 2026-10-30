package workflow

import (
	"testing"

	"legos-factory/factory/internal/fact"
)

func TestEscalateToHumanCarriesForemanClass(t *testing.T) {
	s := newSim(t)
	s.building(pt("a", nil, "a/"))
	nf, err := s.state().Apply(EscalateToHuman("stuck"), s.lim)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nf {
		if n.Kind == KindEscalated {
			if e := n.Payload.(Escalated); e.Class != fact.ClassForeman {
				t.Errorf("escalated = %+v", e)
			}
			return
		}
	}
	t.Fatal("no escalated fact")
}

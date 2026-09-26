package plan

import "testing"

func TestOpaquePlanHandles(t *testing.T) {
	var s Store
	idA, _, err := s.Create(State{Plan: []Item{{Step: "A", Status: "pending"}}})
	if err != nil {
		t.Fatal(err)
	}
	idB, _, err := s.Create(State{Plan: []Item{{Step: "B", Status: "completed"}}})
	if err != nil {
		t.Fatal(err)
	}
	if idA == idB || len(idA) != len("plan_")+32 {
		t.Fatalf("bad handles: %q %q", idA, idB)
	}
	gotA, err := s.Get(idA)
	if err != nil || len(gotA.Plan) != 1 || gotA.Plan[0].Step != "A" {
		t.Fatalf("plan a: %#v err=%v", gotA, err)
	}
	if _, err := s.Update("plan_00000000000000000000000000000000", State{}); err == nil {
		t.Fatal("unknown plan handle unexpectedly updated")
	}
}

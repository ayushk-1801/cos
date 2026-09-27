package plan

import (
	"path/filepath"
	"testing"
)

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

func TestPersistentPlansSurviveReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plans.json")
	s, err := NewPersistent(path)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := s.Create(State{Explanation: "persist me", Plan: []Item{{Step: "A", Status: "in_progress"}}})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := NewPersistent(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s2.Get(id)
	if err != nil || got.Explanation != "persist me" || len(got.Plan) != 1 || got.Plan[0].Status != "in_progress" {
		t.Fatalf("reloaded=%+v err=%v", got, err)
	}
}

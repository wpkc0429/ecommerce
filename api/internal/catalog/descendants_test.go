package catalog

import (
	"sort"
	"testing"

	"ksdevworks/ecommerce/api/internal/ent"
)

func cat(id int, parent *int) *ent.Category { return &ent.Category{ID: id, ParentID: parent} }

func ip(v int) *int { return &v }

func TestDescendantIDs(t *testing.T) {
	// 1 - 2 - 4 - 5   (three levels below the root)
	//  \_ 3           7 is a separate tree
	cats := []*ent.Category{cat(1, nil), cat(2, ip(1)), cat(3, ip(1)), cat(4, ip(2)), cat(5, ip(4)), cat(7, nil)}

	got := descendantIDs(cats, 1)
	sort.Ints(got)
	want := []int{1, 2, 3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("root 1: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("root 1: got %v want %v", got, want)
		}
	}

	if got := descendantIDs(cats, 4); len(got) != 2 {
		t.Fatalf("mid-level node should include itself and its child only, got %v", got)
	}
	if got := descendantIDs(cats, 7); len(got) != 1 || got[0] != 7 {
		t.Fatalf("leaf: got %v", got)
	}
}

func TestDescendantIDsSurvivesCycles(t *testing.T) {
	// Corrupt data: 1 -> 2 -> 3 -> 1. Must terminate and list each node once.
	cats := []*ent.Category{cat(1, ip(3)), cat(2, ip(1)), cat(3, ip(2))}
	got := descendantIDs(cats, 1)
	if len(got) != 3 {
		t.Fatalf("cycle: got %v, want each of 1,2,3 exactly once", got)
	}
}

package als

import "testing"

func TestMergeNodeKeepsAttributeAddedOnTheirs(t *testing.T) {
	base := &Node{Name: "Test", Attrs: []Attr{{Name: "A", Value: "base"}}}
	ours := &Node{Name: "Test", Attrs: []Attr{{Name: "A", Value: "ours"}}}
	theirs := &Node{Name: "Test", Attrs: []Attr{{Name: "A", Value: "base"}, {Name: "B", Value: "theirs"}}}

	got, err := (&merger{h: newHasher()}).node(base, ours, theirs)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if value, ok := got.Attr("A"); !ok || value != "ours" {
		t.Errorf("A = %q, %v; want ours", value, ok)
	}
	if value, ok := got.Attr("B"); !ok || value != "theirs" {
		t.Errorf("B = %q, %v; want theirs", value, ok)
	}
}

func TestMergeNodeConflictsOnDeleteVsEditAttribute(t *testing.T) {
	base := &Node{Name: "Test", Attrs: []Attr{{Name: "A", Value: "base"}}}
	ours := &Node{Name: "Test"}
	theirs := &Node{Name: "Test", Attrs: []Attr{{Name: "A", Value: "theirs"}}}

	if _, err := (&merger{h: newHasher()}).node(base, ours, theirs); err == nil {
		t.Fatal("expected a conflict when one side removes an attribute and the other edits it")
	}
}

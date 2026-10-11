package als

import (
	"strings"
	"testing"
)

// Prawdziwe pliki: ours i theirs to ta sama baza zmieniona osobno - ale
// Merge2 udaje, ze bazy nie zna.
func TestMerge2RealFiles(t *testing.T) {
	o, th := load(t, "ours.als"), load(t, "theirs.als")
	r, err := Merge2(o, th, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Conflicts {
		t.Logf("konflikt: %s (ours %d, theirs %d zmian)", c.UnitLabel, len(c.Ours), len(c.Theirs))
	}
	res := map[string]Resolution{}
	for _, c := range r.Conflicts {
		res[c.ID] = Theirs
	}
	r, err = Merge2(o, th, res)
	if err != nil || r.Doc == nil {
		t.Fatalf("po rozstrzygnieciu: %v, konflikty %d", err, len(r.Conflicts))
	}
	if errs := Validate(r.Doc); len(errs) > 0 {
		t.Fatalf("wynik niespojny: %s", strings.Join(errs, "; "))
	}
	// gitara dodana tylko w ours musi zostac
	if !strings.Contains(string(r.Doc.XMLBytes()), "8-12 String Guitar") {
		t.Error("zgubiona sciezka dodana tylko po jednej stronie")
	}
}

func TestMerge2Identical(t *testing.T) {
	o := load(t, "base.als")
	r, err := Merge2(o, o.Clone(), nil)
	if err != nil || len(r.Conflicts) != 0 || len(Diff(o, r.Doc)) != 0 {
		t.Fatalf("%v, konflikty %d", err, len(r.Conflicts))
	}
}

package als

import (
	"fmt"
	"strconv"
)

// Result to wynik scalania. Przy nierozwiazanych konfliktach Doc == nil.
type Result struct {
	Doc       *Doc
	Conflicts []Conflict
	Warnings  []Warning
	// NoteIDsNormalized: ile nut dostalo z powrotem ID z bazy (dryf ID po
	// zmianach wprowadzonych przez API Live'a), dla diagnostyki.
	NoteIDsNormalized int
}

type Warning struct {
	Kind    string // duplicate-track, ...
	Message string
}

func (w Warning) String() string { return "UWAGA: " + w.Message }

// Merge scala trzy wersje bez rozstrzygania konfliktow.
// Wejsciowe dokumenty nie sa modyfikowane.
func Merge(base, ours, theirs *Doc) (*Result, error) {
	return MergeWith(base, ours, theirs, nil)
}

// MergeWith scala trzy wersje, rozstrzygajac konflikty wg `res`
// (klucz: Conflict.ID). Konflikty bez rozstrzygniecia zostaja w wyniku.
func MergeWith(base, ours, theirs *Doc, res map[string]Resolution) (*Result, error) {
	o, t := ours.Clone(), theirs.Clone()
	r := &Result{}
	r.NoteIDsNormalized = NormalizeNoteIDs(base, o) + NormalizeNoteIDs(base, t)
	if err := remapIDs(base, o, t); err != nil {
		return nil, err
	}

	conflicts := findConflicts(o, t, Diff(base, o), Diff(base, t))
	if len(res) > 0 {
		for _, c := range conflicts {
			choice, ok := res[c.ID]
			if !ok {
				continue
			}
			if err := applyResolution(base, o, t, c, choice); err != nil {
				return nil, fmt.Errorf("%s: %w", c.UnitLabel, err)
			}
		}
		conflicts = findConflicts(o, t, Diff(base, o), Diff(base, t))
	}
	if len(conflicts) > 0 {
		r.Conflicts = conflicts
		return r, nil
	}

	m := &merger{h: newHasher()}
	root, err := m.node(base.Root, o.Root, t.Root)
	if err != nil {
		return nil, err
	}
	r.Doc = &Doc{ProcInst: o.ProcInst, Root: root}
	fixNextPointeeId(r.Doc, o, t)
	fixNoteIdGenerators(r.Doc)
	r.Warnings = duplicateTrackWarnings(base, o, t)
	return r, nil
}

// fixNoteIdGenerators: NoteIdGenerator jest szumem (bierzemy go z ours), wiec
// po scaleniu podnosimy NextId ponad najwieksze NoteId w klipie - inaczej Live
// moglby nadac nowej nucie ID juz uzyte przez nute od drugiej strony.
func fixNoteIdGenerators(d *Doc) {
	d.Root.Walk(func(n *Node) {
		gen := n.Find("NoteIdGenerator", "NextId")
		if n.Name != "Notes" || gen == nil {
			return
		}
		next, _ := strconv.Atoi(valueOf(gen))
		n.Walk(func(ev *Node) {
			if id, ok := ev.Attr("NoteId"); ok {
				if v, err := strconv.Atoi(id); err == nil && v+1 > next {
					next = v + 1
				}
			}
		})
		gen.SetAttr("Value", strconv.Itoa(next))
	})
}

func valueOf(n *Node) string { v, _ := n.Attr("Value"); return v }

// --- Scalanie drzewa -----------------------------------------------------------

type merger struct{ h *hasher }

func (m *merger) node(b, o, t *Node) (*Node, error) {
	switch {
	case m.h.eq(o, t), m.h.eq(b, t):
		return o.Clone(), nil
	case m.h.eq(b, o):
		return t.Clone(), nil
	}
	if atomicElems[b.Name] {
		return nil, fmt.Errorf("%s: obie strony zmienily blob", b.Name)
	}
	res := &Node{Name: o.Name}
	// atrybuty: 3-way per atrybut
	for _, a := range o.Attrs {
		bv, _ := b.Attr(a.Name)
		tv, tok := t.Attr(a.Name)
		v := a.Value
		noise := isNoiseAttr(o.Name, a.Name)
		if tok && !noise && a.Value == bv && tv != bv {
			v = tv
		} else if tok && !noise && a.Value != bv && tv != bv && tv != a.Value {
			return nil, fmt.Errorf("%s@%s: obie strony zmienily", o.Name, a.Name)
		}
		res.Attrs = append(res.Attrs, Attr{a.Name, v})
	}
	res.Text = o.Text
	if o.Text == b.Text {
		res.Text = t.Text
	}
	ch, err := m.children(b, o, t)
	if err != nil {
		return nil, err
	}
	res.Children = ch
	return res, nil
}

func keyed(n *Node) ([]string, map[string]*Node) {
	ks := childKeys(n)
	mp := make(map[string]*Node, len(ks))
	for i, k := range ks {
		mp[k] = n.Children[i]
	}
	return ks, mp
}

// mergeOrder: kolejnosc kluczy wyniku. Bazowo kolejnosc ours, chyba ze tylko
// theirs zmienil kolejnosc wspolnych elementow. Elementy dodane tylko po
// drugiej stronie wstawiamy za ich poprzednikiem z tamtej strony.
func mergeOrder(bk, ok, tk []string, bm, om, tm map[string]*Node) []string {
	primary, secondary := ok, tk
	if sameOrder(bk, ok, bm, om) && !sameOrder(bk, tk, bm, tm) {
		primary, secondary = tk, ok
	}
	keys := append([]string(nil), primary...)
	in := map[string]bool{}
	for _, k := range keys {
		in[k] = true
	}
	for i, k := range secondary {
		if in[k] {
			continue
		}
		if _, inBase := bm[k]; inBase {
			continue // usuniety po stronie primary
		}
		pos := 0
		for j := i - 1; j >= 0; j-- {
			if idx := indexOf(keys, secondary[j]); idx >= 0 {
				pos = idx + 1
				break
			}
		}
		// za elementami, ktore strona primary dodala w tym samym miejscu
		// (deterministycznie: najpierw nowe od primary, potem od secondary)
		for pos < len(keys) && bm[keys[pos]] == nil && indexOf(secondary, keys[pos]) < 0 {
			pos++
		}
		keys = append(keys[:pos], append([]string{k}, keys[pos:]...)...)
		in[k] = true
	}
	return keys
}

func (m *merger) children(b, o, t *Node) ([]*Node, error) {
	bk, bm := keyed(b)
	ok, om := keyed(o)
	tk, tm := keyed(t)

	var out []*Node
	for _, k := range mergeOrder(bk, ok, tk, bm, om, tm) {
		bn, inB := bm[k]
		on, inO := om[k]
		tn, inT := tm[k]
		if inO && isNoiseElem(on) {
			out = append(out, on.Clone()) // szum: zawsze z ours
			continue
		}
		switch {
		case inO && inT && inB:
			n, err := m.node(bn, on, tn)
			if err != nil {
				return nil, err
			}
			out = append(out, n)
		case inO && inT: // dodane po obu stronach
			if !m.h.eq(on, tn) {
				return nil, fmt.Errorf("%s: dodane rozne elementy po obu stronach", k)
			}
			out = append(out, on.Clone())
		case inO && !inB: // dodane w ours
			out = append(out, on.Clone())
		case inT && !inB: // dodane w theirs
			out = append(out, tn.Clone())
		case inO && inB && !inT: // usuniete w theirs
			if !m.h.eq(bn, on) && !isNoiseElem(on) {
				return nil, fmt.Errorf("%s: usuniete w theirs, zmienione w ours", k)
			}
		case inT && inB && !inO: // usuniete w ours
			if !m.h.eq(bn, tn) && !isNoiseElem(tn) {
				return nil, fmt.Errorf("%s: usuniete w ours, zmienione w theirs", k)
			}
		}
	}
	return out, nil
}

func indexOf(s []string, k string) int {
	for i, x := range s {
		if x == k {
			return i
		}
	}
	return -1
}

// --- Ostrzezenia -----------------------------------------------------------------

// duplicateTrackWarnings: obie strony niezaleznie dodaly sciezke o tej samej
// nazwie i z tym samym pierwszym urzadzeniem (zaobserwowane w testdata:
// ta sama gitara dodana osobno w ours i theirs_conflict). Merge zostawia obie
// - to rozne obiekty - ale czlowiek prawie na pewno chcial jednej.
func duplicateTrackWarnings(base, o, t *Doc) []Warning {
	inBase := map[string]bool{}
	for _, tr := range tracks(base) {
		id, _ := tr.Attr("Id")
		inBase[tr.Name+"#"+id] = true
	}
	sig := func(tr *Node) string {
		dev := ""
		if ds := tr.Find("DeviceChain", "DeviceChain", "Devices"); ds != nil && len(ds.Children) > 0 {
			dev = ds.Children[0].Name + ":" + deviceName(ds.Children[0])
		}
		return tr.Name + "|" + trackName(tr) + "|" + dev
	}
	added := map[string]bool{}
	for _, tr := range tracks(o) {
		id, _ := tr.Attr("Id")
		if !inBase[tr.Name+"#"+id] {
			added[sig(tr)] = true
		}
	}
	var out []Warning
	for _, tr := range tracks(t) {
		id, _ := tr.Attr("Id")
		if !inBase[tr.Name+"#"+id] && added[sig(tr)] {
			out = append(out, Warning{"duplicate-track", fmt.Sprintf(
				"obie strony dodaly sciezke %q z tym samym instrumentem - w wyniku sa obie", trackName(tr))})
		}
	}
	return out
}

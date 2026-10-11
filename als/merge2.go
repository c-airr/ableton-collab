package als

import "strconv"

// --- Scalanie dwoch wersji bez wspolnej historii --------------------------------
//
// Przy dolaczaniu do sesji z wlasna kopia projektu nie ma wspolnego przodka:
// nie wiadomo, kto co zmienil. Budujemy sztuczna baze z czesci wspolnej:
//   - element identyczny po obu stronach -> w bazie taki sam (bez zmian),
//   - element tylko po jednej stronie -> brak w bazie (ta strona go "dodala"),
//   - rozne wartosci -> w bazie wartosc-wartownik, rozna od obu (obie strony
//     "zmienily") - konflikt w obejmujacej jednostce (klip, sciezka, ...).
// Potem zwykly MergeWith: dodatki z obu stron wchodza same, roznice wymagaja
// rozstrzygniecia.

// sentinel: wartosc w bazie tam, gdzie strony sie roznia. Widoczna w opisie
// zmian jako "(differs) -> x".
const sentinel = "(differs)"

// Merge2 scala dwie wersje bez wspolnej historii (patrz wyzej).
func Merge2(ours, theirs *Doc, res map[string]Resolution) (*Result, error) {
	return MergeWith(CommonBase(ours, theirs), ours, theirs, res)
}

// CommonBase: sztuczna baza dla Merge2.
func CommonBase(ours, theirs *Doc) *Doc {
	h := newHasher()
	base := &Doc{ProcInst: ours.ProcInst, Root: commonNode(h, ours.Root, theirs.Root)}
	if e := base.Root.Find("LiveSet", "NextPointeeId"); e != nil {
		e.SetAttr("Value", strconv.Itoa(sharedPointeeNext(ours, theirs)))
	}
	return base
}

func commonNode(h *hasher, o, t *Node) *Node {
	if h.eq(o, t) || isNoiseElem(o) {
		return o.Clone()
	}
	n := &Node{Name: o.Name, Text: o.Text}
	if o.Text != t.Text {
		n.Text = sentinel
	}
	for _, a := range o.Attrs {
		v := a.Value
		if tv, ok := t.Attr(a.Name); ok && tv != v && !isNoiseAttr(o.Name, a.Name) {
			v = sentinel
		}
		n.Attrs = append(n.Attrs, Attr{a.Name, v})
	}
	if atomicElems[o.Name] {
		// blob (stan wtyczki): calosc albo nic - wartownik w atrybucie
		n.Children = cloneAll(o.Children)
		n.Attrs = append(n.Attrs, Attr{"AbcollabBase", sentinel})
		return n
	}
	ok, om := keyed(o)
	_, tm := keyed(t)
	for _, k := range ok {
		if tn, in := tm[k]; in {
			n.Children = append(n.Children, commonNode(h, om[k], tn))
		}
	}
	return n
}

func cloneAll(ns []*Node) []*Node {
	out := make([]*Node, len(ns))
	for i, c := range ns {
		out[i] = c.Clone()
	}
	return out
}

// sharedPointeeNext: NextPointeeId sztucznej bazy. Id ponizej sa wspolne
// (ten sam obiekt w tym samym miejscu po obu stronach); od niego w gore
// kazda strona alokowala osobno, wiec remapIDs przesunie nowe Id theirs.
func sharedPointeeNext(ours, theirs *Doc) int {
	where := func(d *Doc) map[string]string {
		out := map[string]string{}
		var walk func(n *Node, path string)
		walk = func(n *Node, path string) {
			if id, ok := n.Attr("Id"); ok {
				out[id] = path
			}
			ks := childKeys(n)
			for i, c := range n.Children {
				walk(c, path+"/"+ks[i])
			}
		}
		walk(d.Root, d.Root.Name)
		return out
	}
	o, t := where(ours), where(theirs)
	next := 0
	for id, p := range t {
		if o[id] == p {
			if v, err := strconv.Atoi(id); err == nil && v+1 > next {
				next = v + 1
			}
		}
	}
	if oN, tN := nextPointee(ours), nextPointee(theirs); next > oN || next > tN {
		next = min(oN, tN)
	}
	if next == 0 {
		next = 1
	}
	return next
}

package als

import (
	"fmt"
	"strings"
)

type Change struct {
	Kind      string   // added, removed, modified, reordered
	Path      []string // klucze od korzenia do zmienionego wezla
	Label     string   // sciezka dla ludzi
	Unit      []string // klucze jednostki konfliktu
	UnitLabel string
	Detail    string
}

func (c Change) String() string {
	s := fmt.Sprintf("%-9s %s", c.Kind, c.Label)
	if c.Detail != "" {
		s += ": " + c.Detail
	}
	return s
}

// Diff zwraca strukturalne zmiany a -> b (bez szumu).
func Diff(a, b *Doc) []Change {
	d := &differ{h: newHasher()}
	root := []string{a.Root.Name}
	d.walk(a.Root, b.Root, 0, root, []seg{{a.Root.Name, false}}, root, a.Root.Name)
	return d.out
}

type seg struct {
	text     string
	landmark bool
}

func fmtLabel(segs []seg) string {
	// Pomijamy Ableton/LiveSet i kontenery (DeviceChain/MainSequencer/...)
	// przed kolejnym "landmarkiem" (sciezka, slot, klip, urzadzenie, nuta).
	// Kontenery za ostatnim landmarkiem zostaja - to one mowia, co sie
	// zmienilo (np. Mixer/Volume/Manual).
	var parts []string
	var generic []string
	for i, s := range segs {
		if i < 2 {
			continue
		}
		if !s.landmark {
			generic = append(generic, s.text)
			continue
		}
		for _, g := range generic {
			if strings.HasPrefix(g, "ArrangerAutomation") {
				parts = append(parts, "Arrangement")
			}
		}
		generic = nil
		parts = append(parts, s.text)
	}
	if len(generic) > 0 {
		parts = append(parts, strings.Join(generic, "/"))
	}
	if len(parts) == 0 {
		return "LiveSet"
	}
	return strings.Join(parts, " › ")
}

type differ struct {
	h   *hasher
	out []Change
}

func (d *differ) add(kind string, path []string, segs []seg, unit []string, unitLabel, detail string) {
	d.out = append(d.out, Change{
		Kind: kind, Path: append([]string(nil), path...), Label: fmtLabel(segs),
		Unit: append([]string(nil), unit...), UnitLabel: unitLabel, Detail: detail,
	})
}

func (d *differ) walk(a, b *Node, depth int, path []string, segs []seg, unit []string, unitLabel string) {
	if d.h.eq(a, b) {
		return
	}
	if atomicElems[a.Name] {
		d.add("modified", path, segs, unit, unitLabel, "blob (stan wtyczki) zmieniony")
		return
	}
	if det := ownDiff(a, b); det != "" {
		d.add("modified", path, segs, unit, unitLabel, det)
	}

	ak, bk := childKeys(a), childKeys(b)
	amap := map[string]*Node{}
	for i, k := range ak {
		amap[k] = a.Children[i]
	}
	bmap := map[string]*Node{}
	for i, k := range bk {
		bmap[k] = b.Children[i]
	}

	child := func(n, parent *Node, k string) ([]string, []seg, []string, string) {
		p := append(append([]string(nil), path...), k)
		s := append(append([]seg(nil), segs...), seg{label(n, parent, k), isLandmark(n, parent)})
		u, ul := unit, unitLabel
		if unitKind(n, parent, depth+1) != "" {
			u, ul = p, fmtLabel(s)
		}
		return p, s, u, ul
	}

	for i, k := range ak {
		c := a.Children[i]
		if isNoiseElem(c) {
			continue
		}
		p, s, u, ul := child(c, a, k)
		if bc, ok := bmap[k]; ok {
			d.walk(c, bc, depth+1, p, s, u, ul)
		} else {
			d.add("removed", p, s, u, ul, summary(c))
		}
	}
	for i, k := range bk {
		c := b.Children[i]
		if isNoiseElem(c) {
			continue
		}
		if _, ok := amap[k]; !ok {
			p, s, u, ul := child(c, b, k)
			d.add("added", p, s, u, ul, summary(c))
		}
	}
	if !sameOrder(ak, bk, amap, bmap) {
		d.add("reordered", path, segs, unit, unitLabel, "zmieniona kolejnosc elementow")
	}
}

// sameOrder: czy wspolne klucze sa w tej samej kolejnosci.
func sameOrder(ak, bk []string, amap, bmap map[string]*Node) bool {
	var x, y []string
	for _, k := range ak {
		if _, ok := bmap[k]; ok && !isNoiseElem(amap[k]) {
			x = append(x, k)
		}
	}
	for _, k := range bk {
		if _, ok := amap[k]; ok && !isNoiseElem(bmap[k]) {
			y = append(y, k)
		}
	}
	return strings.Join(x, "\x00") == strings.Join(y, "\x00")
}

func ownDiff(a, b *Node) string {
	var parts []string
	seen := map[string]bool{}
	for _, at := range a.Attrs {
		seen[at.Name] = true
		if isNoiseAttr(a.Name, at.Name) {
			continue
		}
		bv, ok := b.Attr(at.Name)
		switch {
		case !ok:
			parts = append(parts, at.Name+" usuniety")
		case bv != at.Value:
			parts = append(parts, fmt.Sprintf("%s %s → %s", at.Name, at.Value, bv))
		}
	}
	for _, bt := range b.Attrs {
		if !seen[bt.Name] && !isNoiseAttr(b.Name, bt.Name) {
			parts = append(parts, fmt.Sprintf("%s=%s dodany", bt.Name, bt.Value))
		}
	}
	if a.Text != b.Text {
		parts = append(parts, "tekst zmieniony")
	}
	return strings.Join(parts, ", ")
}

func summary(n *Node) string {
	var parts []string
	for _, a := range n.Attrs {
		if !isNoiseAttr(n.Name, a.Name) {
			parts = append(parts, a.Name+"="+a.Value)
		}
	}
	cnt := 0
	n.Walk(func(*Node) { cnt++ })
	if cnt > 1 {
		parts = append(parts, fmt.Sprintf("(%d elementow)", cnt))
	}
	return strings.Join(parts, " ")
}

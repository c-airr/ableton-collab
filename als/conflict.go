package als

import (
	"fmt"
	"sort"
	"strings"
)

type Resolution int

const (
	Ours Resolution = iota + 1
	Theirs
	Both // tylko klipy w Session View: klip theirs idzie do wolnego slotu
)

func (r Resolution) String() string {
	switch r {
	case Ours:
		return "ours"
	case Theirs:
		return "theirs"
	case Both:
		return "both"
	}
	return "?"
}

func ParseResolution(s string) (Resolution, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "o", "ours", "moje":
		return Ours, nil
	case "t", "theirs", "ich":
		return Theirs, nil
	case "b", "both", "oba":
		return Both, nil
	}
	return 0, fmt.Errorf("nieznany wybor %q (ours/theirs/both)", s)
}

type Conflict struct {
	ID        string   // stabilny klucz do rozstrzygniec (sciezka kluczy)
	Path      []string // wezel, ktory podmienia rozstrzygniecie
	UnitLabel string
	CanBoth   bool // czy dostepne "zachowaj oba"
	Ours      []Change
	Theirs    []Change
}

func (c Conflict) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "KONFLIKT: %s\n", c.UnitLabel)
	for _, ch := range c.Ours {
		fmt.Fprintf(&b, "  ours:   %s\n", ch)
	}
	for _, ch := range c.Theirs {
		fmt.Fprintf(&b, "  theirs: %s\n", ch)
	}
	return b.String()
}

func hasPrefix(p, pre []string) bool {
	if len(pre) > len(p) {
		return false
	}
	for i := range pre {
		if p[i] != pre[i] {
			return false
		}
	}
	return true
}

func eqPath(a, b []string) bool { return len(a) == len(b) && hasPrefix(a, b) }

// clashPath: czy dwie zmiany sie gryza, a jesli tak - ktory wezel obejmuje
// konflikt. Konflikt gdy obie strony ruszyly te sama jednostke albo jedna
// usunela cos, w srodku czego druga cokolwiek zmienila.
func clashPath(a, b Change) ([]string, string, bool) {
	switch {
	case eqPath(a.Unit, b.Unit):
		return a.Unit, a.UnitLabel, true
	case a.Kind == "removed" && hasPrefix(b.Path, a.Path):
		return a.Path, a.Label, true
	case b.Kind == "removed" && hasPrefix(a.Path, b.Path):
		return b.Path, b.Label, true
	}
	return nil, "", false
}

// findConflicts grupuje gryzace sie pary zmian po obejmujacym je wezle.
// Wezel identyczny po obu stronach (ta sama zmiana zrobiona dwa razy albo
// juz rozstrzygniety konflikt) nie jest konfliktem.
func findConflicts(o, t *Doc, oc, tc []Change) []Conflict {
	type pair struct {
		path  []string
		label string
		o, t  Change
	}
	h := newHasher()
	same := map[string]bool{}
	var pairs []pair
	for _, x := range oc {
		for _, y := range tc {
			p, l, ok := clashPath(x, y)
			if !ok {
				continue
			}
			id := strings.Join(p, "/")
			s, seen := same[id]
			if !seen {
				on, tn := nodeAt(o, p), nodeAt(t, p)
				s = (on == nil && tn == nil) || (on != nil && tn != nil && h.eq(on, tn))
				same[id] = s
			}
			if !s {
				pairs = append(pairs, pair{p, l, x, y})
			}
		}
	}
	// najpierw najkrotsze sciezki, zeby konflikty zagniezdzone weszly do
	// obejmujacego je konfliktu
	sort.SliceStable(pairs, func(i, j int) bool { return len(pairs[i].path) < len(pairs[j].path) })
	var out []*Conflict
	for _, p := range pairs {
		var c *Conflict
		for _, g := range out {
			if hasPrefix(p.path, g.Path) {
				c = g
				break
			}
		}
		if c == nil {
			c = &Conflict{ID: strings.Join(p.path, "/"), Path: p.path, UnitLabel: p.label, CanBoth: canBoth(p.path)}
			out = append(out, c)
		}
		c.Ours = appendUnique(c.Ours, p.o)
		c.Theirs = appendUnique(c.Theirs, p.t)
	}
	res := make([]Conflict, len(out))
	for i, c := range out {
		res[i] = *c
	}
	return res
}

func appendUnique(list []Change, c Change) []Change {
	for _, x := range list {
		if eqPath(x.Path, c.Path) && x.Kind == c.Kind && x.Detail == c.Detail {
			return list
		}
	}
	return append(list, c)
}

// canBoth: "zachowaj oba" ma sens tylko dla klipu w slocie Session View.
func canBoth(path []string) bool {
	return slotIndex(path) >= 0 && len(path) > 0 &&
		(strings.HasPrefix(path[len(path)-1], "MidiClip") || strings.HasPrefix(path[len(path)-1], "AudioClip"))
}

// slotIndex: pozycja klucza ClipSlot#n bezposrednio pod ClipSlotList.
func slotIndex(path []string) int {
	for i := 1; i < len(path); i++ {
		if strings.HasPrefix(path[i-1], "ClipSlotList") && strings.HasPrefix(path[i], "ClipSlot#") {
			return i
		}
	}
	return -1
}

// --- Nawigacja po kluczach -------------------------------------------------------

func nodeAt(d *Doc, path []string) *Node {
	if len(path) == 0 || d.Root.Name != path[0] {
		return nil
	}
	cur := d.Root
	for _, k := range path[1:] {
		_, m := keyed(cur)
		if cur = m[k]; cur == nil {
			return nil
		}
	}
	return cur
}

// copyAt ustawia w dst wezel pod `path` na kopie wezla z src (albo go usuwa,
// jesli w src go nie ma).
func copyAt(src, dst *Doc, path []string) error {
	if len(path) < 2 {
		return fmt.Errorf("nie mozna podmienic korzenia")
	}
	k := path[len(path)-1]
	dp := nodeAt(dst, path[:len(path)-1])
	if dp == nil {
		return fmt.Errorf("brak rodzica %s po stronie docelowej", strings.Join(path[:len(path)-1], "/"))
	}
	dk := childKeys(dp)
	idx := indexOf(dk, k)
	sn := nodeAt(src, path)
	switch {
	case sn == nil && idx >= 0:
		dp.Children = append(dp.Children[:idx], dp.Children[idx+1:]...)
	case sn != nil && idx >= 0:
		dp.Children[idx] = sn.Clone()
	case sn != nil:
		// wstaw za najblizszym poprzednikiem ze strony zrodlowej
		sk := childKeys(nodeAt(src, path[:len(path)-1]))
		pos := 0
		for j := indexOf(sk, k) - 1; j >= 0; j-- {
			if i := indexOf(dk, sk[j]); i >= 0 {
				pos = i + 1
				break
			}
		}
		dp.Children = append(dp.Children[:pos], append([]*Node{sn.Clone()}, dp.Children[pos:]...)...)
	}
	return nil
}

func applyResolution(base, o, t *Doc, c Conflict, r Resolution) error {
	switch r {
	case Ours:
		return copyAt(o, t, c.Path)
	case Theirs:
		return copyAt(t, o, c.Path)
	case Both:
		if !c.CanBoth {
			return fmt.Errorf("\"oba\" dziala tylko dla klipow w Session View")
		}
		theirClip := nodeAt(t, c.Path)
		if err := copyAt(o, t, c.Path); err != nil {
			return err
		}
		if theirClip == nil || nodeAt(o, c.Path) == nil {
			return nil // jedna strona klip usunela - "oba" = ten, ktory zostal
		}
		si := slotIndex(c.Path)
		listPath := c.Path[:si]
		inner := c.Path[si+1 : len(c.Path)-1] // ClipSlot[0]/Value[0]
		list := nodeAt(o, listPath)
		for _, sk := range childKeys(list) {
			p := append(append(append([]string(nil), listPath...), sk), inner...)
			if !emptySlot(base, p) || !emptySlot(o, p) || !emptySlot(t, p) {
				continue
			}
			nodeAt(t, p).Children = []*Node{theirClip}
			return nil
		}
		return fmt.Errorf("brak wolnego slotu na tej sciezce - dodaj scene i scal ponownie")
	}
	return fmt.Errorf("nieznane rozstrzygniecie")
}

func emptySlot(d *Doc, valuePath []string) bool {
	v := nodeAt(d, valuePath)
	return v != nil && len(v.Children) == 0
}

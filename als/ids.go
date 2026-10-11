package als

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// --- Kolizje ID ----------------------------------------------------------------
//
// Zaobserwowane: obie strony dodaly sciezke od tej samej bazy (NextPointeeId
// 24006). ours zaalokowal Id 24006..24966, theirs 24006..24544 - te same
// numery dla roznych obiektow (AutomationTarget, ModulationTarget, Pointee...).
// Sciezki tez dostaja Id z osobnego licznika (19 i 20) i moga sie zderzyc.
// Przed scaleniem przesuwamy nowe Id theirs ponad zakres ours.

func nextPointee(d *Doc) int {
	v, _ := strconv.Atoi(d.Root.Value("LiveSet", "NextPointeeId"))
	return v
}

// Elementy, ktorych Value wskazuje na Id pointee (sprawdzone w testdata).
var pointeeRefElems = map[string]bool{"PointeeId": true}

var trackRef = regexp.MustCompile(`Track\.(\d+)`)

func remapIDs(base, ours, theirs *Doc) error {
	bN, oN, tN := nextPointee(base), nextPointee(ours), nextPointee(theirs)
	if bN == 0 {
		return fmt.Errorf("no NextPointeeId in base")
	}
	if oN > bN && tN > bN {
		off := oN - bN
		theirs.Root.Walk(func(n *Node) {
			for i, a := range n.Attrs {
				if a.Name == "Id" || (a.Name == "Value" && pointeeRefElems[n.Name]) {
					if v, err := strconv.Atoi(a.Value); err == nil && v >= bN && v < tN {
						n.Attrs[i].Value = strconv.Itoa(v + off)
					}
				}
			}
		})
		theirs.Root.Find("LiveSet", "NextPointeeId").SetAttr("Value", strconv.Itoa(tN+off))
	}

	// Id sciezek: nowe sciezki theirs, ktorych Id jest zajete w ours
	used := map[string]bool{}
	baseIDs := map[string]bool{}
	maxID := 0
	for _, tr := range tracks(base) {
		id, _ := tr.Attr("Id")
		baseIDs[id] = true
	}
	for _, d := range []*Doc{ours, theirs} {
		for _, tr := range tracks(d) {
			id, _ := tr.Attr("Id")
			if d == ours {
				used[id] = true
			}
			if v, err := strconv.Atoi(id); err == nil && v > maxID {
				maxID = v
			}
		}
	}
	ren := map[string]string{}
	for _, tr := range tracks(theirs) {
		id, _ := tr.Attr("Id")
		if !baseIDs[id] && used[id] {
			maxID++
			ren[id] = strconv.Itoa(maxID)
			tr.SetAttr("Id", ren[id])
		}
	}
	if len(ren) > 0 {
		theirs.Root.Walk(func(n *Node) {
			for i, a := range n.Attrs {
				if n.Name == "TrackGroupId" && a.Name == "Value" {
					if r, ok := ren[a.Value]; ok {
						n.Attrs[i].Value = r
					}
				}
				if a.Name == "Value" && strings.Contains(a.Value, "Track.") {
					n.Attrs[i].Value = trackRef.ReplaceAllStringFunc(a.Value, func(s string) string {
						if r, ok := ren[s[len("Track."):]]; ok {
							return "Track." + r
						}
						return s
					})
				}
			}
		})
	}
	return nil
}

func tracks(d *Doc) []*Node {
	if ts := d.Root.Find("LiveSet", "Tracks"); ts != nil {
		return ts.Children
	}
	return nil
}

func fixNextPointeeId(out, ours, theirs *Doc) {
	n := nextPointee(ours)
	if t := nextPointee(theirs); t > n {
		n = t
	}
	if e := out.Root.Find("LiveSet", "NextPointeeId"); e != nil {
		e.SetAttr("Value", strconv.Itoa(n))
	}
}

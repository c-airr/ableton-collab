package als

import (
	"fmt"
	"strconv"
	"strings"
)

func isPointee(n *Node) bool {
	return n.Name == "Pointee" || strings.HasSuffix(n.Name, "Target") ||
		strings.HasPrefix(n.Name, "ControllerTargets")
}

// Validate sprawdza spojnosc ID po merge'u: unikalnosc Id pointee i sciezek
// oraz czy NextPointeeId jest wieksze od wszystkich uzytych Id.
func Validate(d *Doc) []string {
	var errs []string
	seen := map[string]string{}
	maxID := 0
	d.Root.Walk(func(n *Node) {
		if !isPointee(n) {
			return
		}
		id, ok := n.Attr("Id")
		if !ok {
			return
		}
		if prev, dup := seen[id]; dup {
			errs = append(errs, fmt.Sprintf("duplicate pointee Id %s (%s and %s)", id, prev, n.Name))
		}
		seen[id] = n.Name
		if v, _ := strconv.Atoi(id); v > maxID {
			maxID = v
		}
	})
	if np := nextPointee(d); np <= maxID {
		errs = append(errs, fmt.Sprintf("NextPointeeId %d <= max Id %d", np, maxID))
	}
	tids := map[string]bool{}
	for _, t := range tracks(d) {
		id, _ := t.Attr("Id")
		if tids[id] {
			errs = append(errs, "duplicate track Id "+id)
		}
		tids[id] = true
	}
	return errs
}

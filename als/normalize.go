package als

import "strings"

// pairWalk idzie rownolegle po dwoch drzewach, dopasowujac dzieci po kluczach
// (childKeys), i wola fn dla kazdej pary odpowiadajacych sobie wezlow.
func pairWalk(a, b *Node, fn func(a, b *Node)) {
	fn(a, b)
	_, am := keyed(a)
	bk, _ := keyed(b)
	for i, k := range bk {
		bc := b.Children[i]
		if isNoiseElem(bc) {
			continue
		}
		if ac, ok := am[k]; ok {
			pairWalk(ac, bc, fn)
		}
	}
}

// NormalizeNoteIDs usuwa z `other` dryf identyfikatorow nut wzgledem `base`.
//
// Nuty wstawione przez API Live'a (add_new_notes) dostaja nowe NoteId - nie da
// sie wymusic konkretnego. Po wprowadzeniu zmian kolegi "w locie" ta sama nuta
// ma u nas inne ID niz u niego i bez tej normalizacji wygladalaby jak
// usunieta + dodana (a w nastepnym merge'u dawala falszywe konflikty).
//
// Regula: jesli w klipie `other` jest nuta o ID nieznanym w base, a w base jest
// nuta o tej samej wysokosci i identycznych atrybutach (poza NoteId), ktorej
// w `other` brakuje - to ta sama nuta. Przepisujemy ID (wszystkie atrybuty
// NoteId w obrebie klipu, takze w PerNoteEventStore) na to z base.
// Zwraca liczbe przepisanych nut.
func NormalizeNoteIDs(base, other *Doc) int {
	n := 0
	pairWalk(base.Root, other.Root, func(b, o *Node) {
		if o.Name == "Notes" && o.Child("KeyTracks") != nil {
			n += normalizeClipNotes(b, o)
		}
	})
	return n
}

type noteInfo struct {
	id  string
	sig string // wysokosc + atrybuty bez NoteId
}

func clipNotes(notes *Node) []noteInfo {
	var out []noteInfo
	kts := notes.Child("KeyTracks")
	if kts == nil {
		return nil
	}
	for _, kt := range kts.Children {
		pitch := kt.Value("MidiKey")
		ns := kt.Child("Notes")
		if ns == nil {
			continue
		}
		for _, ev := range ns.Children {
			id, ok := ev.Attr("NoteId")
			if !ok {
				continue
			}
			var sb strings.Builder
			sb.WriteString(pitch)
			for _, a := range ev.Attrs {
				if a.Name != "NoteId" {
					sb.WriteString("\x00" + a.Name + "=" + a.Value)
				}
			}
			out = append(out, noteInfo{id, sb.String()})
		}
	}
	return out
}

func normalizeClipNotes(bNotes, oNotes *Node) int {
	bn, on := clipNotes(bNotes), clipNotes(oNotes)
	inB := map[string]bool{}
	for _, x := range bn {
		inB[x.id] = true
	}
	inO := map[string]bool{}
	for _, x := range on {
		inO[x.id] = true
	}
	// nuty z base, ktorych brakuje w other - kandydaci, pogrupowani po tresci
	free := map[string][]string{}
	for _, x := range bn {
		if !inO[x.id] {
			free[x.sig] = append(free[x.sig], x.id)
		}
	}
	ren := map[string]string{}
	for _, x := range on {
		if inB[x.id] {
			continue
		}
		if ids := free[x.sig]; len(ids) > 0 {
			ren[x.id] = ids[0]
			free[x.sig] = ids[1:]
		}
	}
	if len(ren) == 0 {
		return 0
	}
	oNotes.Walk(func(n *Node) {
		for i, a := range n.Attrs {
			if a.Name == "NoteId" {
				if r, ok := ren[a.Value]; ok {
					n.Attrs[i].Value = r
				}
			}
		}
	})
	return len(ren)
}

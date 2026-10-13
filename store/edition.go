package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/c-airr/ableton-collab/als"
)

// --- Wydanie Live'a (OverwriteProtectionNumber) -------------------------------------
//
// Live przy zapisie wpisuje w kazdy element seta swoj OverwriteProtectionNumber
// (np. Lite 12.4.6 - 3076, Suite/Trial - 3077) i nie nadpisuje pliku z nizsza
// liczba ("created with a version of Live that is older or has fewer
// features... save as a new file") - Lite nie otworzylby wyniku. Przy pracy
// Lite + Suite kazdy zapis na Suite konczylby sie tym oknem, wiec plik
// roboczy dostaje liczbe Live'a z tego komputera (poznana z jego zapisow).
// W diffie to szum.

const opnElem = "OverwriteProtectionNumber"

func (p *Project) livePath() string { return filepath.Join(p.devDir(p.Me.ID), "live.json") }

type liveInfo struct {
	OPN string `json:"opn"` // OverwriteProtectionNumber Live'a na tym komputerze
}

func (p *Project) localOPN() string {
	var li liveInfo
	if data, err := os.ReadFile(p.livePath()); err == nil {
		json.Unmarshal(data, &li)
	}
	if li.OPN == "" {
		li.OPN = p.opnFromHistory()
	}
	return li.OPN
}

// opnFromHistory: liczba z moich wczesniejszych zapisow z Live'a (wersje
// z jednym rodzicem; scalenia sklada abcollab) - dla sesji sprzed live.json.
func (p *Project) opnFromHistory() string {
	vs, err := p.Versions()
	if err != nil {
		return ""
	}
	var mine []*Version
	for _, v := range vs {
		if v.Device == p.Me.ID && len(v.Parents) <= 1 {
			mine = append(mine, v)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Time.After(mine[j].Time) })
	for i, v := range mine {
		if i == 10 {
			break
		}
		d, err := p.LoadDoc(v)
		if err != nil {
			continue
		}
		if opn := uniformOPN(d); opn != "" {
			p.saveOPN(opn)
			return opn
		}
	}
	return ""
}

func uniformOPN(d *als.Doc) string {
	vals := map[string]bool{}
	d.Root.Walk(func(n *als.Node) {
		if n.Name == opnElem {
			v, _ := n.Attr("Value")
			vals[v] = true
		}
	})
	if len(vals) != 1 {
		return ""
	}
	for v := range vals {
		return v
	}
	return ""
}

func (p *Project) saveOPN(opn string) {
	if p.ensureMe() == nil {
		data, _ := json.Marshal(liveInfo{OPN: opn})
		writeFileAtomic(p.livePath(), data)
	}
}

// learnLive: set zapisany przez Live na tym komputerze - Live wpisuje
// wszedzie te sama liczbe; mieszane wartosci (plik zlozony przez abcollab)
// niczego nie ucza.
func (p *Project) learnLive(d *als.Doc) {
	if v := uniformOPN(d); v != "" && v != p.localOPN() {
		p.saveOPN(v)
	}
}

// forLocalLive: set z liczba Live'a z tego komputera (bez zmian, gdy jej
// jeszcze nie znamy albo juz sie zgadza).
func (p *Project) forLocalLive(d *als.Doc) bool {
	opn := p.localOPN()
	if opn == "" {
		return false
	}
	changed := false
	d.Root.Walk(func(n *als.Node) {
		if n.Name == opnElem {
			if v, _ := n.Attr("Value"); v != opn {
				n.SetAttr("Value", opn)
				changed = true
			}
		}
	})
	return changed
}

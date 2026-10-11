package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/c-airr/ableton-collab/als"
)

// LoadDoc wczytuje dokument wersji.
func (p *Project) LoadDoc(v *Version) (*als.Doc, error) {
	data, err := p.ObjectBytes(v)
	if err != nil {
		return nil, err
	}
	return als.Read(bytes.NewReader(data))
}

// diffDocs: zmiany a -> b bez szumu i bez dryfu NoteId.
func diffDocs(a, b *als.Doc) []als.Change {
	b = b.Clone()
	als.NormalizeNoteIDs(a, b)
	return als.Diff(a, b)
}

func summarize(prefix string, ch []als.Change) []string {
	const maxLines, maxLen = 20, 200
	var out []string
	if prefix != "" {
		out = append(out, prefix)
	}
	for i, c := range ch {
		if i == maxLines {
			out = append(out, fmt.Sprintf("... i %d wiecej", len(ch)-maxLines))
			break
		}
		s := c.String()
		if len(s) > maxLen {
			s = s[:maxLen] + "..."
		}
		out = append(out, s)
	}
	return out
}

// readWorking czyta plik roboczy i sprawdza, ze to kompletny .als (Live moze
// byc w trakcie zapisu - wtedy gzip/XML sie nie domknie).
func (p *Project) readWorking() ([]byte, *als.Doc, error) {
	data, err := os.ReadFile(p.WorkingPath())
	if err != nil {
		return nil, nil, err
	}
	doc, err := als.Read(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: nie da sie odczytac (Live jeszcze zapisuje?): %w", p.Meta.Set, err)
	}
	return data, doc, nil
}

// WorkingChanges: zmiany w pliku roboczym wzgledem mojej wersji.
// Bez wersji (head == "") zwraca nil, true.
func (p *Project) WorkingChanges() ([]als.Change, bool, error) {
	head := p.Head()
	if head == "" {
		return nil, true, nil
	}
	_, doc, err := p.readWorking()
	if err != nil {
		return nil, false, err
	}
	vs, err := p.Versions()
	if err != nil {
		return nil, false, err
	}
	hv := vs[head]
	if hv == nil {
		return nil, false, fmt.Errorf("moja wersja %s nie istnieje", Short(head))
	}
	hdoc, err := p.LoadDoc(hv)
	if err != nil {
		return nil, false, err
	}
	ch := diffDocs(hdoc, doc)
	return ch, len(ch) > 0, nil
}

// Commit zapisuje plik roboczy jako nowa wersje. Zwraca nil, gdy wzgledem
// mojej wersji nie ma zmian (sam szum zapisu to brak zmian).
func (p *Project) Commit() (*Version, error) {
	data, doc, err := p.readWorking()
	if err != nil {
		return nil, err
	}
	head := p.Head()
	var parents, summary []string
	if head == "" {
		summary = []string{"pierwsza wersja"}
	} else {
		vs, err := p.Versions()
		if err != nil {
			return nil, err
		}
		hv := vs[head]
		if hv == nil {
			return nil, fmt.Errorf("moja wersja %s nie istnieje", Short(head))
		}
		hdoc, err := p.LoadDoc(hv)
		if err != nil {
			return nil, err
		}
		ch := diffDocs(hdoc, doc)
		if len(ch) == 0 {
			return nil, nil
		}
		parents = []string{head}
		summary = summarize("", ch)
	}
	return p.writeVersion(data, parents, summary)
}

// writeVersion zapisuje obiekt, wersje i przestawia head - w tej kolejnosci,
// zeby lokalnie head nigdy nie wskazywal na niekompletna wersje.
func (p *Project) writeVersion(data []byte, parents, summary []string) (*Version, error) {
	if err := p.ensureMe(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	v := &Version{
		Object:  hex.EncodeToString(sum[:]),
		Parents: parents,
		Device:  p.Me.ID,
		Author:  p.Me.Name,
		Time:    p.now(),
		Summary: summary,
	}
	obj := p.objectPath(v)
	if _, err := os.Stat(obj); err != nil {
		if err := writeFileAtomic(obj, data); err != nil {
			return nil, err
		}
	}
	v.ID = versionID(v)
	if err := writeJSON(filepath.Join(p.devDir(p.Me.ID), "versions", v.ID+".json"), v); err != nil {
		return nil, err
	}
	if err := p.setHead(v.ID); err != nil {
		return nil, err
	}
	return v, nil
}

// --- Status ----------------------------------------------------------------------

type Relation int

const (
	RelNone       Relation = iota // urzadzenie nie ma jeszcze wersji
	RelIncomplete                 // jego wersja jeszcze do nas nie doszla w calosci
	RelSame                       // ta sama wersja
	RelBehind                     // nie ma moich ostatnich zmian (musi pobrac)
	RelAhead                      // ma nowe zmiany, ktore moge pobrac bez scalania
	RelDiverged                   // obie strony maja zmiany - potrzebne scalenie
)

func (r Relation) String() string {
	return [...]string{"brak wersji", "odbieranie...", "aktualny", "nie ma moich zmian", "ma nowe zmiany", "rozne zmiany - do scalenia"}[r]
}

type PeerStatus struct {
	Device   Device
	Head     *Version // nil przy RelNone / RelIncomplete
	Relation Relation
}

type Status struct {
	Head    *Version     // moja wersja (nil - jeszcze zadnej)
	Changes []als.Change // niezapisane jako wersja zmiany w pliku roboczym
	Peers   []PeerStatus
}

func (p *Project) Status() (*Status, error) {
	vs, err := p.Versions()
	if err != nil {
		return nil, err
	}
	st := &Status{}
	me := p.Head()
	if me != "" {
		st.Head = vs[me]
		if st.Head == nil {
			return nil, fmt.Errorf("moja wersja %s nie istnieje", Short(me))
		}
		ch, _, err := p.WorkingChanges()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		st.Changes = ch
	}
	devs, err := p.Devices()
	if err != nil {
		return nil, err
	}
	for _, d := range devs {
		if d.ID == p.Me.ID {
			continue
		}
		ps := PeerStatus{Device: d}
		h := p.readHead(d.ID)
		switch {
		case h == "":
			ps.Relation = RelNone
		case !p.Complete(vs, h):
			ps.Relation = RelIncomplete
		default:
			ps.Head = vs[h]
			ps.Relation = relation(vs, me, h)
		}
		st.Peers = append(st.Peers, ps)
	}
	return st, nil
}

func relation(vs map[string]*Version, me, h string) Relation {
	switch {
	case me == "":
		return RelAhead
	case h == me:
		return RelSame
	case IsAncestor(vs, h, me):
		return RelBehind
	case IsAncestor(vs, me, h):
		return RelAhead
	}
	return RelDiverged
}

// --- Pull ------------------------------------------------------------------------

type PullResult struct {
	Committed     *Version   // moje zmiany zapisane automatycznie przed pobraniem
	FastForwarded []Device   // od kogo przyjeto wersje bez scalania
	Merged        []*Version // utworzone wersje-scalenia
	Warnings      []als.Warning
	Incomplete    []Device // wersje jeszcze w drodze
	Backup        string   // kopia pliku roboczego (dolaczanie z wlasna zawartoscia)
	// Konflikt przy scalaniu z ConflictWith - nic dla tego urzadzenia nie
	// zostalo zapisane; Pull z rozstrzygnieciami (Conflict.ID) dokonczy.
	Conflicts    []als.Conflict
	ConflictWith Device
}

// WorkingChanged: czy plik roboczy zostal podmieniony (Live musi go przeladowac).
func (r *PullResult) WorkingChanged() bool { return len(r.FastForwarded) > 0 || len(r.Merged) > 0 }

// Pull przyjmuje zmiany od wszystkich urzadzen: najpierw zapisuje moje
// niezapisane zmiany jako wersje, potem po kolei przewija (fast-forward) albo
// scala. Konflikt zatrzymuje scalanie na tym urzadzeniu.
func (p *Project) Pull(res map[string]als.Resolution) (*PullResult, error) {
	r := &PullResult{}
	me := p.Head()
	if me != "" {
		v, err := p.Commit()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if v != nil {
			r.Committed, me = v, v.ID
		}
	}
	vs, err := p.Versions()
	if err != nil {
		return nil, err
	}
	devs, err := p.Devices()
	if err != nil {
		return nil, err
	}
	sort.Slice(devs, func(i, j int) bool { return devs[i].ID < devs[j].ID })
	for _, d := range devs {
		if d.ID == p.Me.ID {
			continue
		}
		h := p.readHead(d.ID)
		if h == "" {
			continue
		}
		if !p.Complete(vs, h) {
			r.Incomplete = append(r.Incomplete, d)
			continue
		}
		switch relation(vs, me, h) {
		case RelSame, RelBehind:
			continue
		case RelAhead:
			if me == "" {
				if err := p.backupForeignWorking(vs[h], r); err != nil {
					return nil, err
				}
			}
			if err := p.checkout(vs[h]); err != nil {
				return nil, err
			}
			me = h
			r.FastForwarded = append(r.FastForwarded, d)
		case RelDiverged:
			v, conflicts, warns, err := p.merge(vs, me, h, d, res)
			if err != nil {
				return nil, fmt.Errorf("scalanie z %s: %w", d.Name, err)
			}
			if len(conflicts) > 0 {
				r.Conflicts, r.ConflictWith = conflicts, d
				return r, nil
			}
			vs[v.ID] = v
			me = v.ID
			r.Merged = append(r.Merged, v)
			r.Warnings = append(r.Warnings, warns...)
		}
	}
	return r, nil
}

func (p *Project) checkout(v *Version) error {
	data, err := p.ObjectBytes(v)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(p.WorkingPath(), data); err != nil {
		return err
	}
	return p.setHead(v.ID)
}

// backupForeignWorking: dolaczam do projektu, a w folderze juz lezy set z inna
// trescia - nie nadpisujemy go bez kopii.
func (p *Project) backupForeignWorking(target *Version, r *PullResult) error {
	data, doc, err := p.readWorking()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		data, _ = os.ReadFile(p.WorkingPath()) // nieczytelny - tez kopiujemy
	} else {
		tdoc, err := p.LoadDoc(target)
		if err != nil {
			return err
		}
		if len(diffDocs(tdoc, doc)) == 0 {
			return nil
		}
	}
	name := strings.TrimSuffix(p.Meta.Set, filepath.Ext(p.Meta.Set))
	r.Backup = filepath.Join(p.Dir, fmt.Sprintf("%s (kopia %s).als", name, p.now().Local().Format("2006-01-02 150405")))
	return writeFileAtomic(r.Backup, data)
}

func (p *Project) merge(vs map[string]*Version, me, h string, d Device, res map[string]als.Resolution) (*Version, []als.Conflict, []als.Warning, error) {
	base := MergeBase(vs, me, h)
	if base == "" {
		return nil, nil, nil, fmt.Errorf("brak wspolnej historii")
	}
	var docs [3]*als.Doc
	for i, id := range []string{base, me, h} {
		doc, err := p.LoadDoc(vs[id])
		if err != nil {
			return nil, nil, nil, err
		}
		docs[i] = doc
	}
	mr, err := als.MergeWith(docs[0], docs[1], docs[2], res)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(mr.Conflicts) > 0 {
		return nil, mr.Conflicts, nil, nil
	}
	if errs := als.Validate(mr.Doc); len(errs) > 0 {
		return nil, nil, nil, fmt.Errorf("wynik niespojny: %s", strings.Join(errs, "; "))
	}
	data, err := mr.Doc.Encode()
	if err != nil {
		return nil, nil, nil, err
	}
	// Najpierw plik roboczy, potem wersja: gdyby zapis pliku sie nie udal
	// (np. zablokowany), head zostaje na mojej wersji i nastepny commit nie
	// uzna starego pliku za "cofniecie" zmian drugiej strony.
	if err := writeFileAtomic(p.WorkingPath(), data); err != nil {
		return nil, nil, nil, err
	}
	summary := summarize("scalenie z "+d.Name, diffDocs(docs[1], mr.Doc))
	v, err := p.writeVersion(data, []string{me, h}, summary)
	if err != nil {
		return nil, nil, nil, err
	}
	return v, nil, mr.Warnings, nil
}

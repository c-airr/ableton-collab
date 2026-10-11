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
	"time"

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
			out = append(out, fmt.Sprintf("... and %d more", len(ch)-maxLines))
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
		return nil, nil, fmt.Errorf("%s: can't read the set (is Live still saving?): %w", p.SetName(), err)
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
		return nil, false, fmt.Errorf("my version %s does not exist", Short(head))
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
		summary = []string{"first version"}
	} else {
		vs, err := p.Versions()
		if err != nil {
			return nil, err
		}
		hv := vs[head]
		if hv == nil {
			return nil, fmt.Errorf("my version %s does not exist", Short(head))
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
	return [...]string{"no version yet", "receiving...", "up to date", "missing your changes", "has new changes", "diverged - needs merge"}[r]
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
			return nil, fmt.Errorf("my version %s does not exist", Short(me))
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
	// Adopt: daty zapisu obu stron - okno wyboru zaznacza nowsza.
	OursTime, TheirsTime time.Time
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
				return nil, fmt.Errorf("merging with %s: %w", d.Name, err)
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
	// set moze lezec w podfolderze, ktory u nas jeszcze nie istnieje
	if err := os.MkdirAll(filepath.Dir(p.WorkingPath()), 0o755); err != nil {
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
	name := strings.TrimSuffix(p.WorkingPath(), filepath.Ext(p.WorkingPath()))
	r.Backup = filepath.Join(fmt.Sprintf("%s (copy %s).als", name, p.now().Local().Format("2006-01-02 150405")))
	return writeFileAtomic(r.Backup, data)
}

func (p *Project) merge(vs map[string]*Version, me, h string, d Device, res map[string]als.Resolution) (*Version, []als.Conflict, []als.Warning, error) {
	base := MergeBase(vs, me, h)
	if base == "" {
		return nil, nil, nil, fmt.Errorf("no common history")
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
		return nil, nil, nil, fmt.Errorf("merged set is inconsistent: %s", strings.Join(errs, "; "))
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
	summary := summarize("merge with "+d.Name, diffDocs(docs[1], mr.Doc))
	v, err := p.writeVersion(data, []string{me, h}, summary)
	if err != nil {
		return nil, nil, nil, err
	}
	return v, nil, mr.Warnings, nil
}

// --- Dolaczanie z wlasna kopia ------------------------------------------------------

// Adopt: dolaczam do sesji, majac juz wlasna kopie projektu (local - sciezka
// bezwzgledna do mojego seta). Nie ma wspolnej historii, wiec scalamy dwie
// wersje (als.Merge2): to, co jest tylko u jednej strony, wchodzi samo, a
// elementy rozne po obu stronach wymagaja rozstrzygniecia (res). Wynik staje
// sie moja pierwsza wersja z rodzicem = wersja zapraszajacego.
func (p *Project) Adopt(local string, res map[string]als.Resolution) (*PullResult, error) {
	if p.Head() != "" {
		return nil, fmt.Errorf("you already have a version of this session")
	}
	vs, err := p.Versions()
	if err != nil {
		return nil, err
	}
	host, hv := p.newestPeerHead(vs)
	if hv == nil {
		return &PullResult{Incomplete: []Device{host}}, nil
	}
	hdoc, err := p.LoadDoc(hv)
	if err != nil {
		return nil, err
	}
	ldata, err := os.ReadFile(local)
	if err != nil {
		return nil, err
	}
	ldoc, err := als.Read(bytes.NewReader(ldata))
	if err != nil {
		return nil, fmt.Errorf("%s: can't read the set (is Live still saving?): %w", filepath.Base(local), err)
	}
	mr, err := als.Merge2(ldoc, hdoc, res)
	if err != nil {
		return nil, err
	}
	r := &PullResult{ConflictWith: host}
	if fi, err := os.Stat(local); err == nil {
		r.OursTime = fi.ModTime()
	}
	r.TheirsTime = hv.Time
	if len(mr.Conflicts) > 0 {
		r.Conflicts = mr.Conflicts
		return r, nil
	}
	if errs := als.Validate(mr.Doc); len(errs) > 0 {
		return nil, fmt.Errorf("merged set is inconsistent: %s", strings.Join(errs, "; "))
	}
	data, err := mr.Doc.Encode()
	if err != nil {
		return nil, err
	}
	// moj set przed scaleniem zostaje obok (sety nie sa synchronizowane)
	name := strings.TrimSuffix(local, filepath.Ext(local))
	r.Backup = fmt.Sprintf("%s (before merge %s).als", name, p.now().Local().Format("2006-01-02 150405"))
	if err := writeFileAtomic(r.Backup, ldata); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p.WorkingPath()), 0o755); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(p.WorkingPath(), data); err != nil {
		return nil, err
	}
	v, err := p.writeVersion(data, []string{hv.ID}, summarize("merged my copy with "+host.Name, diffDocs(hdoc, mr.Doc)))
	if err != nil {
		return nil, err
	}
	r.Merged = []*Version{v}
	r.Warnings = mr.Warnings
	return r, nil
}

// newestPeerHead: najnowsza kompletna wersja innych uczestnikow (przy
// dolaczaniu - zwykle zapraszajacego).
func (p *Project) newestPeerHead(vs map[string]*Version) (Device, *Version) {
	devs, _ := p.Devices()
	var best *Version
	var who Device
	for _, d := range devs {
		if d.ID == p.Me.ID {
			continue
		}
		h := p.readHead(d.ID)
		if h == "" {
			continue
		}
		who = d
		if !p.Complete(vs, h) {
			continue
		}
		if v := vs[h]; best == nil || v.Time.After(best.Time) {
			best, who = v, d
		}
	}
	return who, best
}

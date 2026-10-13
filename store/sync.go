package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	head := p.Loaded()
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
// wersji, z ktorej pochodzi tresc pliku, nie ma zmian (sam szum zapisu to
// brak zmian).
func (p *Project) Commit() (*Version, error) {
	cr, err := p.commit(nil, false)
	if err != nil {
		return nil, err
	}
	if len(cr.conflicts) > 0 {
		return nil, fmt.Errorf("the saved set conflicts with newer changes (%d) - use Get changes to choose", len(cr.conflicts))
	}
	return cr.v, nil
}

type commitResult struct {
	v         *Version // nowa wersja z pliku roboczego (nil - bez zmian)
	merged    *Version // scalenie z moim head (plik pochodzil ze starszej wersji)
	warnings  []als.Warning
	conflicts []als.Conflict // nic nie zapisane - commit z rozstrzygnieciami dokonczy
}

// fileParent: wersja, z ktorej pochodzi tresc pliku roboczego - wg znacznika
// w secie (patrz marker.go); sety bez znacznika maja wlasna linie ("") od
// Loaded.
func (p *Project) fileParent(vs map[string]*Version, doc *als.Doc) (marker, parent string) {
	marker = docBase(doc)
	if id := p.lineage()[marker]; id != "" && vs[id] != nil {
		return marker, id
	}
	if marker != "" && vs[marker] != nil {
		return marker, marker
	}
	return marker, p.Loaded()
}

// commit: plik roboczy jako wersja z rodzicem = wersja, z ktorej pochodzi jego
// tresc. Gdy to nie moj head (Live nie przeladowal podmienionego pliku albo
// dostawal zmiany na zywo), od razu scalamy z head - zapis starej tresci nie
// cofa zmian, ktorych Live nie mial. writeWorking: wynik scalenia do pliku
// roboczego (Live nie ma go otwartego).
func (p *Project) commit(res map[string]als.Resolution, writeWorking bool) (*commitResult, error) {
	cr := &commitResult{}
	data, doc, err := p.readWorking()
	if err != nil {
		return nil, err
	}
	vs, err := p.Versions()
	if err != nil {
		return nil, err
	}
	head := p.Head()
	marker, parent := p.fileParent(vs, doc)
	if parent == "" {
		p.learnLive(doc)
		v, err := p.writeVersion(data, nil, []string{"first version"})
		if err != nil {
			return nil, err
		}
		cr.v = v
		return cr, p.setLineage(marker, v.ID)
	}
	pv := vs[parent]
	if pv == nil {
		return nil, fmt.Errorf("my version %s does not exist", Short(parent))
	}
	pdoc, err := p.LoadDoc(pv)
	if err != nil {
		return nil, err
	}
	ch := diffDocs(pdoc, doc)
	if len(ch) == 0 {
		return cr, nil
	}
	p.learnLive(doc) // zmiany - plik zapisal Live z tego komputera
	behind := head != "" && vs[head] != nil && !IsAncestor(vs, head, parent)
	if behind {
		// najpierw sprawdzamy, czy scalenie sie uda - przy konflikcie nic nie
		// zapisujemy (commit z rozstrzygnieciami zrobi to od nowa)
		base := MergeBase(vs, parent, head)
		if base == "" {
			return nil, fmt.Errorf("no common history with my version %s", Short(head))
		}
		bdoc, err := p.LoadDoc(vs[base])
		if err != nil {
			return nil, err
		}
		hdoc, err := p.LoadDoc(vs[head])
		if err != nil {
			return nil, err
		}
		mr, err := als.MergeWith(bdoc, doc, hdoc, res)
		if err != nil {
			return nil, err
		}
		if len(mr.Conflicts) > 0 {
			cr.conflicts = mr.Conflicts
			return cr, nil
		}
	}
	v, err := p.writeVersion(data, []string{parent}, summarize("", ch))
	if err != nil {
		return nil, err
	}
	cr.v = v
	if err := p.setLineage(marker, v.ID); err != nil {
		return nil, err
	}
	if !behind {
		return cr, nil
	}
	vs[v.ID] = v
	m, conflicts, warns, err := p.merge(vs, v.ID, head, p.Me, res, writeWorking)
	if err != nil {
		return nil, fmt.Errorf("merging with my newer version: %w", err)
	}
	if len(conflicts) > 0 { // sprawdzone wyzej - nie powinno sie zdarzyc
		cr.conflicts = conflicts
		return cr, nil
	}
	cr.merged, cr.warnings = m, warns
	return cr, nil
}

func (p *Project) newVersion(data []byte, parents, summary []string) *Version {
	sum := sha256.Sum256(data)
	v := &Version{
		Object:  hex.EncodeToString(sum[:]),
		Parents: parents,
		Device:  p.Me.ID,
		Author:  p.Me.Name,
		Time:    p.now(),
		Summary: summary,
	}
	v.ID = versionID(v)
	return v
}

// writeVersion zapisuje obiekt, wersje i przestawia head - w tej kolejnosci,
// zeby lokalnie head nigdy nie wskazywal na niekompletna wersje.
func (p *Project) writeVersion(data []byte, parents, summary []string) (*Version, error) {
	return p.saveVersion(p.newVersion(data, parents, summary), data)
}

func (p *Project) saveVersion(v *Version, data []byte) (*Version, error) {
	if err := p.ensureMe(); err != nil {
		return nil, err
	}
	obj := p.objectPath(v)
	if _, err := os.Stat(obj); err != nil {
		if err := writeFileAtomic(obj, data); err != nil {
			return nil, err
		}
	}
	if err := writeJSON(filepath.Join(p.devDir(p.Me.ID), "versions", v.ID+".json"), v); err != nil {
		return nil, err
	}
	if err := p.setHead(v.ID); err != nil {
		return nil, err
	}
	// to, co zapisalem, jest w pliku roboczym - Live ma to wczytane
	if err := p.setLoaded(v.ID); err != nil {
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
	// Live: PullLive - plik roboczy nie byl podmieniany.
	Live bool
}

// WorkingChanged: czy przyszly zmiany (przy Pull plik roboczy zostal
// podmieniony i Live musi go przeladowac).
func (r *PullResult) WorkingChanged() bool { return len(r.FastForwarded) > 0 || len(r.Merged) > 0 }

// Pull przyjmuje zmiany od wszystkich urzadzen: najpierw zapisuje moje
// niezapisane zmiany jako wersje, potem po kolei przewija (fast-forward) albo
// scala. Konflikt zatrzymuje scalanie na tym urzadzeniu.
func (p *Project) Pull(res map[string]als.Resolution) (*PullResult, error) {
	return p.pull(res, false)
}

// PullLive: jak Pull, ale nie podmienia pliku roboczego - Live jest otwarty i
// dostaje zmiany na zywo (Remote Script). head idzie do przodu, Loaded
// zostaje; czego nie dalo sie przeniesc na zywo, pokazuje PendingReload.
func (p *Project) PullLive(res map[string]als.Resolution) (*PullResult, error) {
	return p.pull(res, true)
}

func (p *Project) pull(res map[string]als.Resolution, live bool) (*PullResult, error) {
	r := &PullResult{Live: live}
	me := p.Head()
	if me != "" {
		cr, err := p.commit(res, !live)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if cr != nil {
			if len(cr.conflicts) > 0 {
				r.Conflicts, r.ConflictWith = cr.conflicts, p.Me
				return r, nil
			}
			r.Committed = cr.v
			if cr.merged != nil {
				r.Merged = append(r.Merged, cr.merged)
				r.Warnings = append(r.Warnings, cr.warnings...)
			}
			me = p.Head()
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
			if live && me != "" {
				if err := p.setHead(h); err != nil {
					return nil, err
				}
			} else {
				if me == "" {
					if err := p.backupForeignWorking(vs[h], r); err != nil {
						return nil, err
					}
				}
				if err := p.checkout(vs[h]); err != nil {
					return nil, err
				}
			}
			me = h
			r.FastForwarded = append(r.FastForwarded, d)
		case RelDiverged:
			v, conflicts, warns, err := p.merge(vs, me, h, d, res, !live)
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
	if err := p.writeWorkingVersion(data, v.ID); err != nil {
		return err
	}
	if err := p.setHead(v.ID); err != nil {
		return err
	}
	return p.setLoaded(v.ID)
}

// writableDir: folder istnieje i da sie w nim pisac. Folder projektu Live'a
// przyniesiony z Windowsa bywa "tylko do odczytu" (atrybut od Desktop.ini,
// przeniesiony przez Syncthing jako r-x) - wtedy przywracamy prawo zapisu.
func writableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o200 == 0 {
		return os.Chmod(dir, fi.Mode().Perm()|0o700)
	}
	return nil
}

// Reload: plik roboczy = head (po przeladowaniu w Live ma wszystko, rowniez
// zmiany, ktorych nie dalo sie przeniesc na zywo).
func (p *Project) Reload() error {
	vs, err := p.Versions()
	if err != nil {
		return err
	}
	v := vs[p.Head()]
	if v == nil {
		return fmt.Errorf("no version to reload")
	}
	return p.checkout(v)
}

// PendingReload: zmiany w head, ktorych nie ma w wersji wczytanej w Live
// (czesc z nich mogla dojsc na zywo - wolajacy odsiewa je sam).
func (p *Project) PendingReload() ([]als.Change, error) {
	head, loaded := p.Head(), p.Loaded()
	if head == "" || head == loaded {
		return nil, nil
	}
	vs, err := p.Versions()
	if err != nil {
		return nil, err
	}
	if vs[head] == nil || vs[loaded] == nil {
		return nil, nil
	}
	a, err := p.LoadDoc(vs[loaded])
	if err != nil {
		return nil, err
	}
	b, err := p.LoadDoc(vs[head])
	if err != nil {
		return nil, err
	}
	return diffDocs(a, b), nil
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

func (p *Project) merge(vs map[string]*Version, me, h string, d Device, res map[string]als.Resolution, writeWorking bool) (*Version, []als.Conflict, []als.Warning, error) {
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
	prefix := "merge with " + d.Name
	if d.ID == p.Me.ID {
		prefix = "merge with my newer version"
	}
	v := p.newVersion(data, []string{me, h}, summarize(prefix, diffDocs(docs[1], mr.Doc)))
	// Najpierw plik roboczy, potem wersja: gdyby zapis pliku sie nie udal
	// (np. zablokowany), head zostaje na mojej wersji i nastepny commit nie
	// uzna starego pliku za "cofniecie" zmian drugiej strony.
	if writeWorking {
		if err := p.writeWorkingVersion(data, v.ID); err != nil {
			return nil, nil, nil, err
		}
	}
	loaded := p.Loaded()
	v, err = p.saveVersion(v, data)
	if err != nil {
		return nil, nil, nil, err
	}
	// writeVersion przestawia Loaded na nowa wersje - przy pracy na zywo Live
	// jej nie wczytal, wiec wracamy do poprzedniej
	if !writeWorking {
		if err := p.setLoaded(loaded); err != nil {
			return nil, nil, nil, err
		}
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
	if err := writableDir(filepath.Dir(p.WorkingPath())); err != nil {
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

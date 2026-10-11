// Package store trzyma historie wersji projektu Live w zwyklym folderze.
//
// Uklad (wewnatrz folderu projektu, synchronizowany dowolnym transportem):
//
//	.abcollab/
//	  project.json                 id projektu i nazwa pliku seta (.als)
//	  devices/<deviceID>/
//	    device.json                nazwa urzadzenia (do wyswietlania)
//	    objects/<sha256>.als       bajty pliku .als, nazwa = sha256 zawartosci
//	    versions/<id>.json         wersja: obiekt, rodzice, autor, czas, podsumowanie
//	    head                       id ostatniej wersji, na ktorej stoi urzadzenie
//
// Kazde urzadzenie pisze WYLACZNIE do swojego katalogu devices/<id>/, wiec
// transport (Syncthing) nigdy nie widzi dwoch rownoleglych zapisow tego samego
// pliku. Wersje tworza DAG (jak commity w gicie): zwykla wersja ma jednego
// rodzica, scalenie dwoch. Wersje innych urzadzen czytamy z ich katalogow.
//
// Transport dostarcza pliki w dowolnej kolejnosci i pojedynczo: head moze
// przyjsc przed wersja, a wersja przed obiektem. Dlatego wszystko, co czytamy
// od innych, jest sprawdzane (istnienie, sha256) i niekompletne wersje sa
// pomijane do czasu, az dojda brakujace pliki.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/c-airr/ableton-collab/als"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const Dir = ".abcollab"

type Device struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Peer: ID urzadzenia w transporcie (Syncthing) - lobby laczy po nim stan
	// polaczenia ze stanem wersji. Puste, gdy projekt chodzi bez transportu.
	Peer string `json:"peer,omitempty"`
}

type Meta struct {
	ID string `json:"id"`
	// Host: ID w transporcie (Syncthing) osoby, ktora zalozyla sesje - tylko
	// ona moze wyrzucac uczestnikow.
	Host string `json:"host,omitempty"`
	// Set: sciezka seta (.als) wzgledem folderu projektu, z "/" niezaleznie od
	// systemu. Moze byc w podfolderze - Live przy "Save As" do zwyklego
	// folderu zaklada w nim "<nazwa> Project/".
	Set     string    `json:"set"`
	Created time.Time `json:"created"`
}

type Version struct {
	ID      string    `json:"-"`
	Object  string    `json:"object"`
	Parents []string  `json:"parents"`
	Device  string    `json:"device"`
	Author  string    `json:"author"`
	Time    time.Time `json:"time"`
	Summary []string  `json:"summary,omitempty"`
}

func (v *Version) Short() string { return Short(v.ID) }

func Short(id string) string {
	if len(id) > 10 {
		return id[:10]
	}
	return id
}

type Project struct {
	Dir  string // folder projektu Live
	Me   Device
	Meta Meta
	// Now pozwala testom ustawic zegar.
	Now func() time.Time
}

var ErrNotProject = errors.New("not an abcollab session (no " + Dir + "/project.json)")

func (p *Project) root() string            { return filepath.Join(p.Dir, Dir) }
func (p *Project) devDir(id string) string { return filepath.Join(p.root(), "devices", id) }

// WorkingPath: plik .als, na ktorym pracuje Live.
func (p *Project) WorkingPath() string { return filepath.Join(p.Dir, filepath.FromSlash(p.Meta.Set)) }

// SetName: nazwa pliku seta (bez podfolderu).
func (p *Project) SetName() string { return path.Base(p.Meta.Set) }

// validSet: wzgledna sciezka .als wewnatrz projektu. Przychodzi tez od innych
// (project.json), wiec bez "..", sciezek absolutnych i "\" (przenosnosc).
func validSet(set string) bool {
	if set == "" || strings.ContainsAny(set, "\\:") || path.IsAbs(set) || !strings.EqualFold(path.Ext(set), ".als") {
		return false
	}
	for _, part := range strings.Split(set, "/") {
		if part == "" || part == "." || part == ".." || part == Dir {
			return false
		}
	}
	return true
}

func newID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToUpper(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// NewDevice tworzy nowa tozsamosc urzadzenia (losowe ID).
func NewDevice(name string) Device { return Device{ID: newID(10), Name: name} }

// Init zaklada projekt w folderze z istniejacym setem i zapisuje pierwsza wersje.
func Init(dir, set string, me Device) (*Project, *Version, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil, err
	}
	if _, err := os.Stat(filepath.Join(dir, Dir, "project.json")); err == nil {
		return nil, nil, fmt.Errorf("%s is already an abcollab session", dir)
	}
	set = filepath.ToSlash(set)
	if !validSet(set) {
		return nil, nil, fmt.Errorf("the set must be an .als file inside the session folder, got %q", set)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(set))); err != nil {
		return nil, nil, err
	}
	p := &Project{Dir: dir, Me: me, Meta: Meta{ID: newID(10), Host: me.Peer, Set: set, Created: time.Now().UTC()}}
	if err := os.MkdirAll(p.root(), 0o755); err != nil {
		return nil, nil, err
	}
	if err := writeJSON(filepath.Join(p.root(), "project.json"), p.Meta); err != nil {
		return nil, nil, err
	}
	v, err := p.Commit()
	if err != nil {
		return nil, nil, err
	}
	return p, v, nil
}

// Open otwiera istniejacy projekt (np. zsynchronizowany od kogos innego).
func Open(dir string, me Device) (*Project, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	p := &Project{Dir: dir, Me: me}
	if err := readJSON(filepath.Join(p.root(), "project.json"), &p.Meta); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotProject
		}
		return nil, err
	}
	if !validSet(p.Meta.Set) {
		return nil, fmt.Errorf("project.json: invalid set path %q", p.Meta.Set)
	}
	return p, nil
}

func (p *Project) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

// ensureMe zaklada katalog urzadzenia przy pierwszym zapisie.
func (p *Project) ensureMe() error {
	d := p.devDir(p.Me.ID)
	for _, sub := range []string{"objects", "versions"} {
		if err := os.MkdirAll(filepath.Join(d, sub), 0o755); err != nil {
			return err
		}
	}
	return writeJSON(filepath.Join(d, "device.json"), p.Me)
}

// Announce zapisuje moje device.json (nazwa, ID w transporcie), jesli sie
// zmienilo - inni widza wtedy, kim jestem, zanim zapisze pierwsza wersje.
func (p *Project) Announce() error {
	var cur Device
	if readJSON(filepath.Join(p.devDir(p.Me.ID), "device.json"), &cur) == nil && cur == p.Me {
		return nil
	}
	return p.ensureMe()
}

// --- Odczyt -----------------------------------------------------------------------

// Devices zwraca urzadzenia, ktore cos zapisaly w projekcie (lacznie ze mna).
func (p *Project) Devices() ([]Device, error) {
	ents, err := os.ReadDir(filepath.Join(p.root(), "devices"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var out []Device
	for _, e := range ents {
		if !e.IsDir() || !validID(e.Name()) {
			continue
		}
		d := Device{ID: e.Name(), Name: e.Name()}
		var meta Device
		if readJSON(filepath.Join(p.devDir(e.Name()), "device.json"), &meta) == nil {
			if meta.Name != "" {
				d.Name = meta.Name
			}
			d.Peer = meta.Peer
		}
		out = append(out, d)
	}
	return out, nil
}

// validID: ID z cudzych plikow trafiaja do sciezek - tylko bezpieczne znaki.
func validID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// Versions wczytuje wersje wszystkich urzadzen. Wersje, ktorych plik nie
// zgadza sie z ID (uszkodzony / w trakcie synchronizacji), sa pomijane.
func (p *Project) Versions() (map[string]*Version, error) {
	devs, err := p.Devices()
	if err != nil {
		return nil, err
	}
	out := map[string]*Version{}
	for _, d := range devs {
		dir := filepath.Join(p.devDir(d.ID), "versions")
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			id, ok := strings.CutSuffix(e.Name(), ".json")
			if !ok || !validID(id) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			var v Version
			if json.Unmarshal(data, &v) != nil || versionID(&v) != id || v.Device != d.ID {
				continue
			}
			v.ID = id
			out[id] = &v
		}
	}
	return out, nil
}

// versionID = sha256 z kanonicznej tresci wersji (bez podsumowania).
func versionID(v *Version) string {
	h := sha256.New()
	fmt.Fprintf(h, "object %s\ndevice %s\ntime %s\n", v.Object, v.Device, v.Time.UTC().Format(time.RFC3339Nano))
	for _, par := range v.Parents {
		fmt.Fprintf(h, "parent %s\n", par)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (p *Project) objectPath(v *Version) string {
	return filepath.Join(p.devDir(v.Device), "objects", v.Object+".als")
}

// ObjectBytes czyta obiekt wersji i sprawdza jego sha256.
func (p *Project) ObjectBytes(v *Version) ([]byte, error) {
	if !validID(v.Object) {
		return nil, fmt.Errorf("version %s: invalid object", v.Short())
	}
	data, err := os.ReadFile(p.objectPath(v))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != v.Object {
		return nil, fmt.Errorf("version %s: object incomplete or damaged", v.Short())
	}
	return data, nil
}

// Complete: wersja i cala jej historia sa dostepne lokalnie (wersje + obiekty).
func (p *Project) Complete(vs map[string]*Version, id string) bool {
	seen := map[string]bool{}
	stack := []string{id}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		v, ok := vs[cur]
		if !ok {
			return false
		}
		if _, err := os.Stat(p.objectPath(v)); err != nil {
			return false
		}
		stack = append(stack, v.Parents...)
	}
	return true
}

func (p *Project) readHead(devID string) string {
	data, err := os.ReadFile(filepath.Join(p.devDir(devID), "head"))
	if err != nil {
		return ""
	}
	h := strings.TrimSpace(string(data))
	if !validID(h) {
		return ""
	}
	return h
}

// Head: wersja, na ktorej stoje ("" - jeszcze zadnej).
func (p *Project) Head() string { return p.readHead(p.Me.ID) }

func (p *Project) setHead(id string) error {
	if err := p.ensureMe(); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(p.devDir(p.Me.ID), "head"), []byte(id+"\n"))
}

// Loaded: wersja, ktora Live ma wczytana (plik roboczy to ona plus zmiany
// jeszcze niezapisane jako wersja). Przy pracy na zywo (PullLive) head idzie
// do przodu bez podmiany pliku, wiec Loaded bywa starsza niz head - moj
// nastepny zapis z Live'a ma ja za rodzica, a scalenie z head zachowuje
// zmiany innych, ktorych Live nie wczytal (zwykly zapis by je "cofnal").
func (p *Project) Loaded() string {
	data, err := os.ReadFile(filepath.Join(p.devDir(p.Me.ID), "loaded"))
	if err == nil {
		if id := strings.TrimSpace(string(data)); validID(id) {
			return id
		}
	}
	return p.Head()
}

func (p *Project) setLoaded(id string) error {
	if err := p.ensureMe(); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(p.devDir(p.Me.ID), "loaded"), []byte(id+"\n"))
}

// --- DAG ----------------------------------------------------------------------------

func ancestors(vs map[string]*Version, id string) map[string]bool {
	out := map[string]bool{}
	stack := []string{id}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if out[cur] {
			continue
		}
		out[cur] = true
		if v, ok := vs[cur]; ok {
			stack = append(stack, v.Parents...)
		}
	}
	return out
}

// IsAncestor: czy a jest przodkiem b (albo a == b).
func IsAncestor(vs map[string]*Version, a, b string) bool { return ancestors(vs, b)[a] }

// MergeBase: najblizszy wspolny przodek. Przy kilku rownorzednych (criss-cross)
// bierzemy najnowszy - prostsze niz rekurencyjny merge gita, a w praktyce
// wystarcza, bo i tak scalamy strukturalnie.
func MergeBase(vs map[string]*Version, a, b string) string {
	aa, ab := ancestors(vs, a), ancestors(vs, b)
	var common []string
	for id := range aa {
		if ab[id] && vs[id] != nil {
			common = append(common, id)
		}
	}
	var best []string
	for _, c := range common {
		dominated := false
		for _, o := range common {
			if o != c && IsAncestor(vs, c, o) {
				dominated = true
				break
			}
		}
		if !dominated {
			best = append(best, c)
		}
	}
	if len(best) == 0 {
		return ""
	}
	sort.Slice(best, func(i, j int) bool {
		if !vs[best[i]].Time.Equal(vs[best[j]].Time) {
			return vs[best[i]].Time.After(vs[best[j]].Time)
		}
		return best[i] < best[j]
	})
	return best[0]
}

// Log zwraca wersje od najnowszej.
func Log(vs map[string]*Version) []*Version {
	out := make([]*Version, 0, len(vs))
	for _, v := range vs {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.After(out[j].Time)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// --- Pomocnicze --------------------------------------------------------------------

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'))
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

var writeFileAtomic = als.WriteFileAtomic

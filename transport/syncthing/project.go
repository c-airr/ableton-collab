package syncthing

import (
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// MaxMembers: ile osob (razem z nami) moze byc w jednym projekcie.
const MaxMembers = 10

// FolderID: folder Syncthinga projektu = id projektu abcollab.
func FolderID(projectID string) string { return "abcollab-" + strings.ToLower(projectID) }

// ShortID: poczatek ID urzadzenia do porownania na glos przy akceptacji.
func ShortID(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	return id
}

// --- zaproszenie ---------------------------------------------------------------------

// Invite: wszystko, czego gosc potrzebuje, zeby poprosic o dolaczenie. Nie ma
// w nim sekretu - zabezpieczeniem jest akceptacja po stronie zapraszajacego.
type Invite struct {
	Device string `json:"d"` // ID Syncthinga zapraszajacego
	Folder string `json:"f"`
	Name   string `json:"n"` // nazwa projektu (do wyswietlenia i nazwy folderu)
	Host   string `json:"h"` // nazwa zapraszajacego
}

const invitePrefix = "ABC1"

// Code: same litery i cyfry, zeby kod dalo sie zaznaczyc dwuklikiem i wkleic
// przez komunikator bez psucia formatowania.
func (i Invite) Code() string {
	data, _ := json.Marshal(i)
	return invitePrefix + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(data)
}

var (
	deviceIDRe = regexp.MustCompile(`^[A-Z2-7]{7}(-[A-Z2-7]{7}){7}$`)
	folderIDRe = regexp.MustCompile(`^abcollab-[a-z2-7]{1,64}$`)
)

func ParseInvite(s string) (Invite, error) {
	var inv Invite
	// zostaja tylko litery i cyfry: kod skopiowany z terminala bywa pociety na
	// linie, z ramka okienka albo z cudzyslowami z komunikatora
	s = strings.ToUpper(strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s))
	body, ok := strings.CutPrefix(s, invitePrefix)
	if !ok {
		return inv, errors.New("that is not an abcollab invite code (it should start with " + invitePrefix + ")")
	}
	data, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(body)
	if err != nil || json.Unmarshal(data, &inv) != nil {
		return inv, errors.New("the invite code is cut off or mistyped - copy the whole code again")
	}
	if !deviceIDRe.MatchString(inv.Device) || !folderIDRe.MatchString(inv.Folder) {
		return inv, errors.New("the invite code is invalid")
	}
	inv.Name = cleanName(inv.Name, "session")
	inv.Host = cleanName(inv.Host, "host")
	return inv, nil
}

// cleanName: nazwa z cudzego kodu trafia do nazwy folderu i na ekran.
func cleanName(s, def string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return -1
		}
		return r
	}, s)
	s = strings.Trim(strings.TrimSpace(s), ".")
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60])
	}
	if s == "" {
		return def
	}
	return s
}

// --- folder projektu -----------------------------------------------------------------

// ignores: caly folder sesji jest synchronizowany (sample, nagrania, mp3...),
// ale zadne sety (.als) - kazdy ma swoj plik roboczy, a zmiany ida jako
// wersje w .abcollab/ i sa scalane przez abcollab. Inaczej Syncthing
// nadpisywalby sobie nawzajem sety i tworzyl pliki sync-conflict.
// Kolejnosc ma znaczenie (wygrywa pierwszy pasujacy wzorzec): wersje w
// .abcollab/ tez maja koncowke .als, wiec sa wlaczone przed "*.als".
const ignores = `// abcollab: do not edit - rewritten on every start
*.tmp
!/.abcollab
*.als
Backup
Ableton Project Info
(?i)desktop.ini
(?i)thumbs.db
.DS_Store
`

func writeIgnores(dir string) error {
	return os.WriteFile(filepath.Join(dir, ".stignore"), []byte(ignores), 0o644)
}

// Project: folder projektu udostepniony przez Syncthing.
type Project struct {
	n      *Node
	Folder string
	Dir    string
	label  string
}

// Share udostepnia folder projektu (zaklada go w Syncthingu przy pierwszym
// razie). Wywolywane przy kazdym otwarciu projektu.
func (n *Node) Share(dir, folderID, label string) (*Project, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := writeIgnores(dir); err != nil {
		return nil, err
	}
	p := &Project{n: n, Folder: folderID, Dir: dir, label: label}
	f, err := n.findFolder(folderID)
	if err != nil {
		return nil, err
	}
	if f != nil {
		if !samePath(f.Path, dir) {
			return nil, fmt.Errorf("this session is already shared from another folder: %s", f.Path)
		}
		if f.FsWatcherDelayS != 1 || !f.IgnorePerms {
			if err := n.PatchFolder(folderID, map[string]any{"fsWatcherDelayS": 1, "ignorePerms": true}); err != nil {
				return nil, err
			}
		}
		return p, nil
	}
	return p, n.AddFolder(FolderConfig{ID: folderID, Label: label, Path: dir, Devices: []FolderDevice{{DeviceID: n.ID}}})
}

// Join: gosc dodaje zapraszajacego i zaklada u siebie folder projektu juz
// wspoldzielony z nim. Zapraszajacy widzi wtedy prosbe o dolaczenie; po
// akceptacji Syncthing od razu zaczyna synchronizacje (folder ma to samo ID).
// Zapraszajacy jest "introducerem": przedstawia nam pozostalych uczestnikow.
// existing: folder z wlasnymi plikami (scalanie) - jego zawartosc poleci do
// innych, wiec wolajacy musi go wczesniej sprawdzic (tui.InspectFolder);
// bez tego tylko pusty albo nowy folder.
func (n *Node) Join(inv Invite, dir string, existing bool) (*Project, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if inv.Device == n.ID {
		return nil, errors.New("that is your own invite code - send it to someone else")
	}
	if f, err := n.findFolder(inv.Folder); err != nil {
		return nil, err
	} else if f != nil {
		return nil, fmt.Errorf("you already joined this session: %s", f.Path)
	}
	// Tylko pusty albo nowy folder: Syncthing wyslalby zapraszajacemu
	// wszystko, co juz w nim lezy (np. cale Dokumenty po pomylce).
	if ents, err := os.ReadDir(dir); err == nil && len(ents) > 0 && !existing {
		return nil, fmt.Errorf("folder %s is not empty - pick a new or empty folder", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := writeIgnores(dir); err != nil {
		return nil, err
	}
	if err := n.AddDevice(DeviceConfig{DeviceID: inv.Device, Name: inv.Host, Introducer: true}); err != nil {
		return nil, err
	}
	f := FolderConfig{ID: inv.Folder, Label: inv.Name, Path: dir,
		Devices: []FolderDevice{{DeviceID: n.ID}, {DeviceID: inv.Device}}}
	if err := n.AddFolder(f); err != nil {
		return nil, err
	}
	return &Project{n: n, Folder: inv.Folder, Dir: dir, label: inv.Name}, nil
}

func (n *Node) findFolder(id string) (*FolderConfig, error) {
	fs, err := n.Folders()
	if err != nil {
		return nil, err
	}
	for _, f := range fs {
		if f.ID == id {
			return &f, nil
		}
	}
	return nil, nil
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	return a == b || strings.EqualFold(a, b) && os.PathSeparator == '\\'
}

// Invite: kod zaproszenia do tego projektu (prosby akceptuje ten, kto zaprosil).
func (p *Project) Invite(myName string) string {
	return Invite{Device: p.n.ID, Folder: p.Folder, Name: p.label, Host: myName}.Code()
}

func (p *Project) MyID() string { return p.n.ID }

// --- lobby ---------------------------------------------------------------------------

type Member struct {
	ID        string
	Name      string
	Connected bool
	Via       string // "LAN", "internet", "relay" - jak idzie polaczenie
}

type Request struct {
	ID      string
	Name    string
	Address string
	Time    time.Time
}

type Lobby struct {
	Me       string
	Members  []Member // bez nas
	Requests []Request
	Sync     *FolderStatus
}

// Full: lobby osiagnelo MaxMembers (my + inni).
func (l *Lobby) Full() bool { return len(l.Members)+1 >= MaxMembers }

func (p *Project) Lobby() (*Lobby, error) {
	f, err := p.n.Folder(p.Folder)
	if err != nil {
		return nil, err
	}
	devs, err := p.n.Devices()
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, d := range devs {
		names[d.DeviceID] = d.Name
	}
	conns, err := p.n.Connections()
	if err != nil {
		return nil, err
	}
	l := &Lobby{Me: p.n.ID}
	in := map[string]bool{}
	for _, fd := range f.Devices {
		in[fd.DeviceID] = true
		if fd.DeviceID == p.n.ID {
			continue
		}
		m := Member{ID: fd.DeviceID, Name: names[fd.DeviceID]}
		if m.Name == "" {
			m.Name = ShortID(fd.DeviceID)
		}
		if c, ok := conns[fd.DeviceID]; ok && c.Connected {
			m.Connected, m.Via = true, via(c)
		}
		l.Members = append(l.Members, m)
	}
	pend, err := p.n.PendingDevices()
	if err != nil {
		return nil, err
	}
	for id, pd := range pend {
		if !in[id] {
			l.Requests = append(l.Requests, Request{ID: id, Name: cleanName(pd.Name, ShortID(id)), Address: pd.Address, Time: pd.Time})
		}
	}
	sort.Slice(l.Members, func(i, j int) bool { return l.Members[i].Name < l.Members[j].Name })
	sort.Slice(l.Requests, func(i, j int) bool { return l.Requests[i].Time.Before(l.Requests[j].Time) })
	if st, err := p.n.FolderStatus(p.Folder); err == nil {
		l.Sync = st
	}
	return l, nil
}

func via(c Connection) string {
	if strings.HasPrefix(c.Type, "relay") {
		return "relay"
	}
	host, _, err := net.SplitHostPort(c.Address)
	if err != nil {
		host = c.Address
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
		return "LAN"
	}
	return "internet"
}

// Accept przyjmuje prosbe: dodaje urzadzenie i udostepnia mu folder projektu.
func (p *Project) Accept(r Request) error {
	f, err := p.n.Folder(p.Folder)
	if err != nil {
		return err
	}
	for _, d := range f.Devices {
		if d.DeviceID == r.ID {
			return nil
		}
	}
	if len(f.Devices) >= MaxMembers {
		return fmt.Errorf("the session is full (%d people)", MaxMembers)
	}
	// urzadzenie moze juz byc znane z innego projektu - wtedy nie ruszamy jego ustawien
	devs, err := p.n.Devices()
	if err != nil {
		return err
	}
	known := false
	for _, d := range devs {
		known = known || d.DeviceID == r.ID
	}
	if !known {
		if err := p.n.AddDevice(DeviceConfig{DeviceID: r.ID, Name: r.Name}); err != nil {
			return err
		}
	}
	return p.n.SetFolderDevices(p.Folder, append(f.Devices, FolderDevice{DeviceID: r.ID}))
}

// Reject odrzuca prosbe na stale (urzadzenie trafia na liste ignorowanych).
func (p *Project) Reject(r Request) error {
	if err := p.n.IgnoreDevice(r.ID, r.Name, r.Address); err != nil {
		return err
	}
	p.n.DismissPendingDevice(r.ID)
	return nil
}

// Kick usuwa uczestnika z sesji: przestajemy mu udostepniac folder i
// ignorujemy jego prosby. Pozostali, ktorzy znaja go przez nas
// (introducer), tez go usuna.
func (p *Project) Kick(m Member) error {
	f, err := p.n.Folder(p.Folder)
	if err != nil {
		return err
	}
	var keep []FolderDevice
	for _, d := range f.Devices {
		if d.DeviceID != m.ID {
			keep = append(keep, d)
		}
	}
	if err := p.n.SetFolderDevices(p.Folder, keep); err != nil {
		return err
	}
	// urzadzenie usuwamy calkiem, jesli nie dzielimy z nim innej sesji
	fs, err := p.n.Folders()
	if err != nil {
		return err
	}
	shared := false
	for _, f := range fs {
		for _, d := range f.Devices {
			shared = shared || d.DeviceID == m.ID
		}
	}
	if !shared {
		if err := p.n.RemoveDevice(m.ID); err != nil {
			return err
		}
	}
	return p.n.IgnoreDevice(m.ID, m.Name, "")
}

// Leave: przestajemy synchronizowac sesje (pliki zostaja na dysku).
func (p *Project) Leave() error { return p.n.RemoveFolder(p.Folder) }

// --- zdarzenia -----------------------------------------------------------------------

// Event: zdarzenie Syncthinga dotyczace sesji (do logu).
// Kind: "received", "sending", "deleted", "connected",
// "disconnected", "request", "error".
type Event struct {
	ID   int
	Time time.Time
	Kind string
	File string // sciezka wzgledem folderu sesji ("/")
	Who  string // nazwa urzadzenia
}

// Events czeka (long-poll do timeout) na zdarzenia nowsze niz since i zwraca
// te dotyczace tej sesji. Drugi wynik: ID do nastepnego wywolania.
func (p *Project) Events(since int, timeout time.Duration) ([]Event, int, error) {
	raw, err := p.n.RawEvents(since, timeout)
	if err != nil {
		return nil, since, err
	}
	names := map[string]string{}
	if devs, err := p.n.Devices(); err == nil {
		for _, d := range devs {
			names[d.DeviceID] = d.Name
		}
	}
	who := func(id string) string {
		if n := names[id]; n != "" {
			return n
		}
		return ShortID(id)
	}
	next := since
	var out []Event
	for _, e := range raw {
		next = max(next, e.ID)
		d := e.Data
		ev := Event{ID: e.ID, Time: e.Time}
		folder, _ := d["folder"].(string)
		mine := folder == p.Folder
		switch e.Type {
		case "ItemFinished":
			item, _ := d["item"].(string)
			if !mine || internal(item) {
				continue
			}
			ev.File = slash(item)
			switch {
			case d["error"] != nil:
				ev.Kind = "error"
			case d["action"] == "delete":
				ev.Kind = "deleted"
			default:
				ev.Kind = "received"
			}
		case "LocalIndexUpdated":
			if !mine {
				continue
			}
			files, _ := d["filenames"].([]any)
			for _, f := range files {
				name, _ := f.(string)
				if name == "" || internal(name) {
					continue
				}
				out = append(out, Event{ID: e.ID, Time: e.Time, Kind: "sending", File: slash(name)})
			}
			continue
		case "DeviceConnected", "DeviceDisconnected":
			id, _ := d["id"].(string)
			if !p.member(id) {
				continue
			}
			ev.Who = who(id)
			ev.Kind = map[string]string{"DeviceConnected": "connected", "DeviceDisconnected": "disconnected"}[e.Type]
		case "PendingDevicesChanged":
			added, _ := d["added"].([]any)
			for _, a := range added {
				m, _ := a.(map[string]any)
				name, _ := m["name"].(string)
				id, _ := m["deviceID"].(string)
				out = append(out, Event{ID: e.ID, Time: e.Time, Kind: "request", Who: cleanName(name, ShortID(id))})
			}
			continue
		default:
			continue
		}
		out = append(out, ev)
	}
	return out, next, nil
}

// slash: Syncthing na Windowsie podaje sciezki z "\\".
func slash(name string) string { return strings.ReplaceAll(name, "\\", "/") }

// internal: pliki abcollab/Syncthinga - nie pokazujemy ich w logu.
func internal(name string) bool {
	name = slash(name)
	return strings.HasPrefix(name, ".abcollab") || strings.HasPrefix(name, ".st") || strings.Contains(name, "/.st")
}

func (p *Project) member(id string) bool {
	f, err := p.n.Folder(p.Folder)
	if err != nil {
		return false
	}
	for _, d := range f.Devices {
		if d.DeviceID == id {
			return true
		}
	}
	return false
}

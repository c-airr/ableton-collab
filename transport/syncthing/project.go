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
		return inv, errors.New("to nie jest kod zaproszenia abcollab (powinien zaczynac sie od " + invitePrefix + ")")
	}
	data, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(body)
	if err != nil || json.Unmarshal(data, &inv) != nil {
		return inv, errors.New("kod zaproszenia jest uciety albo przekrecony - skopiuj go jeszcze raz w calosci")
	}
	if !deviceIDRe.MatchString(inv.Device) || !folderIDRe.MatchString(inv.Folder) {
		return inv, errors.New("kod zaproszenia ma niepoprawna tresc")
	}
	inv.Name = cleanName(inv.Name, "projekt")
	inv.Host = cleanName(inv.Host, "zapraszajacy")
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

// ignores: plik roboczy Live'a NIE jest synchronizowany - kazdy ma swoj, a
// zmiany ida jako wersje w .abcollab/ i sa scalane przez abcollab. Inaczej
// Syncthing nadpisywalby sobie nawzajem sety i tworzyl pliki sync-conflict.
const ignores = `// abcollab: nie edytuj - plik nadpisywany przy kazdym starcie
/*.als
/Backup
/Ableton Project Info
*.tmp
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
			return nil, fmt.Errorf("ten projekt jest juz udostepniany z innego folderu: %s", f.Path)
		}
		return p, nil
	}
	return p, n.AddFolder(FolderConfig{ID: folderID, Label: label, Path: dir, Devices: []FolderDevice{{DeviceID: n.ID}}})
}

// Join: gosc dodaje zapraszajacego i zaklada u siebie folder projektu juz
// wspoldzielony z nim. Zapraszajacy widzi wtedy prosbe o dolaczenie; po
// akceptacji Syncthing od razu zaczyna synchronizacje (folder ma to samo ID).
// Zapraszajacy jest "introducerem": przedstawia nam pozostalych uczestnikow.
func (n *Node) Join(inv Invite, dir string) (*Project, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if inv.Device == n.ID {
		return nil, errors.New("to twoj wlasny kod zaproszenia - wyslij go koledze")
	}
	if f, err := n.findFolder(inv.Folder); err != nil {
		return nil, err
	} else if f != nil {
		return nil, fmt.Errorf("juz dolaczyles do tego projektu: %s", f.Path)
	}
	// Tylko pusty albo nowy folder: Syncthing wyslalby zapraszajacemu
	// wszystko, co juz w nim lezy (np. cale Dokumenty po pomylce).
	if ents, err := os.ReadDir(dir); err == nil && len(ents) > 0 {
		return nil, fmt.Errorf("folder %s nie jest pusty - wybierz nowy albo pusty folder na projekt", dir)
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
		return fmt.Errorf("lobby pelne (%d osob)", MaxMembers)
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

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/c-airr/ableton-collab/internal/tui"
	"github.com/c-airr/ableton-collab/store"
	"github.com/c-airr/ableton-collab/transport/syncthing"

	tea "github.com/charmbracelet/bubbletea"
)

// --- tozsamosc i stan ------------------------------------------------------------------

func configDir() (string, error) {
	if h := os.Getenv("ABCOLLAB_HOME"); h != "" {
		return h, nil
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "abcollab"), nil
}

func device() (store.Device, error) {
	d, _, err := loadDevice()
	return d, err
}

// loadDevice: tozsamosc tego komputera; isNew - wlasnie utworzona (pierwsze
// uruchomienie, panel zapyta o nick).
func loadDevice() (d store.Device, isNew bool, err error) {
	dir, err := configDir()
	if err != nil {
		return d, false, err
	}
	path := filepath.Join(dir, "device.json")
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &d); err != nil || d.ID == "" {
			return d, false, fmt.Errorf("%s is damaged", path)
		}
		return d, false, nil
	}
	name, _ := os.Hostname()
	if name == "" {
		name = "computer"
	}
	d = store.NewDevice(name)
	return d, true, saveDevice(path, d)
}

func saveDevice(path string, d store.Device) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(d, "", "  ")
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// appState: to, co panel pamieta miedzy uruchomieniami.
type appState struct {
	Last string `json:"last,omitempty"` // sesja otwierana przy starcie
}

func statePath() string {
	dir, _ := configDir()
	return filepath.Join(dir, "state.json")
}

func loadState() appState {
	var s appState
	if data, err := os.ReadFile(statePath()); err == nil {
		json.Unmarshal(data, &s)
	}
	return s
}

func saveState(s appState) {
	data, _ := json.MarshalIndent(s, "", "  ")
	os.WriteFile(statePath(), append(data, '\n'), 0o644)
}

// --- Syncthing ------------------------------------------------------------------------------

func startNode(d *store.Device) (*syncthing.Node, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	exe, err := syncthing.Ensure(filepath.Join(cache, "abcollab"), func(s string) { fmt.Fprintln(os.Stderr, s) })
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(os.Stderr, "starting Syncthing...")
	n, err := syncthing.Start(syncthing.Options{Home: filepath.Join(dir, "syncthing"), Exe: exe, Name: d.Name})
	if err != nil {
		return nil, err
	}
	if d.Peer != n.ID {
		d.Peer = n.ID
		if err := saveDevice(filepath.Join(dir, "device.json"), *d); err != nil {
			n.Stop()
			return nil, err
		}
	}
	return n, nil
}

// share udostepnia sesje w Syncthingu i oglasza moje ID transportu w projekcie.
func share(n *syncthing.Node, p *store.Project) (*syncthing.Project, error) {
	sh, err := n.Share(p.Dir, syncthing.FolderID(p.Meta.ID), filepath.Base(p.Dir))
	if err != nil {
		return nil, err
	}
	p.Me.Peer = n.ID
	return sh, p.Announce()
}

// --- backend panelu -------------------------------------------------------------------------

type backend struct {
	node *syncthing.Node // nil: -offline
	me   *store.Device
}

func (b *backend) DeviceID() string {
	if b.node == nil {
		return ""
	}
	return b.node.ID
}

func (b *backend) Rename(name string) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	b.me.Name = name
	if err := saveDevice(filepath.Join(dir, "device.json"), *b.me); err != nil {
		return err
	}
	if b.node != nil {
		return b.node.PatchDevice(b.node.ID, map[string]any{"name": name})
	}
	return nil
}

func (b *backend) Sessions() ([]tui.SessionRef, error) {
	if b.node == nil {
		return nil, nil
	}
	fs, err := b.node.Folders()
	if err != nil {
		return nil, err
	}
	var out []tui.SessionRef
	for _, f := range fs {
		if strings.HasPrefix(f.ID, "abcollab-") {
			out = append(out, tui.SessionRef{Name: f.Label, Dir: f.Path})
		}
	}
	return out, nil
}

// network: *syncthing.Project jako tui.Network (nil bez sieci, nie typowany nil).
func network(p *syncthing.Project) tui.Network {
	if p == nil {
		return nil
	}
	return p
}

func (b *backend) Open(dir string) (*store.Project, tui.Network, error) {
	p, err := store.Open(dir, *b.me)
	if errors.Is(err, store.ErrNotProject) {
		return nil, nil, fmt.Errorf("%s isn't a session yet (still joining? reopen it once the host accepted you)", dir)
	}
	if err != nil {
		return nil, nil, err
	}
	if b.node == nil {
		return p, nil, nil
	}
	sh, err := share(b.node, p)
	if err != nil {
		return nil, nil, err
	}
	return p, network(sh), nil
}

func (b *backend) Host(dir, set string) (*store.Project, tui.Network, error) {
	p, err := store.Open(dir, *b.me)
	switch {
	case err == nil:
		if set != "" && p.Meta.Set != set {
			return nil, nil, fmt.Errorf("this folder is already a session with set %s", p.Meta.Set)
		}
	case errors.Is(err, store.ErrNotProject):
		if p, _, err = store.Init(dir, set, *b.me); err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, err
	}
	if b.node == nil {
		return p, nil, nil
	}
	sh, err := share(b.node, p)
	if err != nil {
		return nil, nil, err
	}
	return p, network(sh), nil
}

func (b *backend) Join(inv syncthing.Invite, dir string, existing bool) (tui.Network, error) {
	if b.node == nil {
		return nil, errors.New("offline - joining needs the network")
	}
	p, err := b.node.Join(inv, dir, existing)
	if err != nil {
		return nil, err
	}
	return network(p), nil
}

func (b *backend) SetLast(dir string) {
	s := loadState()
	s.Last = dir
	saveState(s)
}

// --- panel ----------------------------------------------------------------------------------

func defaultProjectsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "Documents", "Ableton", "abcollab")
}

func browseFrom() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	for _, d := range []string{filepath.Join(home, "Documents", "Ableton"), filepath.Join(home, "Documents")} {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d
		}
	}
	return home
}

// runTUI: panel. dir (z -C) - od razu ta sesja; inaczej ostatnio otwarta.
func runTUI(dir string, offline bool, code string) error {
	d, isNew, err := loadDevice()
	if err != nil {
		return err
	}
	var node *syncthing.Node
	if !offline {
		if node, err = startNode(&d); err != nil {
			return fmt.Errorf("%w\n(without the network, inside a session folder: abcollab -offline)", err)
		}
		defer node.Stop()
	}
	last := loadState().Last
	if dir != "" {
		last = dir
	}
	if last != "" {
		if _, err := os.Stat(last); err != nil {
			last = ""
		}
	}
	be := &backend{node: node, me: &d}
	app := tui.New(be, tui.Options{Me: d, AskName: isNew, Last: last, BaseDir: defaultProjectsDir(),
		BrowseFrom: browseFrom(), Offline: offline, JoinCode: code})
	_, err = tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

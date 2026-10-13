package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/c-airr/ableton-collab/als"
	"github.com/c-airr/ableton-collab/internal/live"
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
	Last     string        `json:"last,omitempty"` // sesja otwierana przy starcie
	Settings *tui.Settings `json:"settings,omitempty"`
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
	node  *syncthing.Node // nil: -offline
	me    *store.Device
	token string // sekret skryptu AbletonCollab ("" - bez pracy na zywo)
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

func (b *backend) Settings() tui.Settings {
	if s := loadState().Settings; s != nil {
		return *s
	}
	return tui.Settings{AutoSync: true, AutoSave: live.AutoSaveDefault}
}

func (b *backend) SetSettings(set tui.Settings) error {
	s := loadState()
	s.Settings = &set
	saveState(s)
	return nil
}

func (b *backend) SaveInLive() (bool, error) { return live.SaveInLive() }

// liveSync: silnik i jego polaczenie ze skryptem (zamykane razem).
type liveSync struct {
	*live.Engine
	cl *live.Client
}

func (l liveSync) Close() { l.Engine.Close(); l.cl.Close() }

func (b *backend) Live(p *store.Project) tui.LiveSync {
	if b.token == "" || b.node == nil {
		return nil
	}
	names := func(dev string) string {
		if ds, err := p.Devices(); err == nil {
			for _, d := range ds {
				if d.ID == dev {
					return d.Name
				}
			}
		}
		return dev
	}
	tracks := func() []string {
		doc, err := als.ReadFile(p.WorkingPath())
		if err != nil {
			return nil
		}
		return als.TrackNames(doc)
	}
	cl := live.Dial(live.Addr, b.token)
	return liveSync{Engine: live.Start(cl, filepath.Join(p.Dir, store.Dir), p.Me.ID, p.Meta.ID, names, tracks), cl: cl}
}

func (b *backend) SetLast(dir string) {
	s := loadState()
	s.Last = dir
	saveState(s)
}

// --- panel ----------------------------------------------------------------------------------

// documentsDir: folder Dokumentow. Na Linuksie wg ustawien pulpitu
// (~/.config/user-dirs.dirs) - po polsku to ~/Dokumenty, nie ~/Documents.
func documentsDir(home string) string {
	if runtime.GOOS == "linux" {
		conf := os.Getenv("XDG_CONFIG_HOME")
		if conf == "" {
			conf = filepath.Join(home, ".config")
		}
		if data, err := os.ReadFile(filepath.Join(conf, "user-dirs.dirs")); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				v, ok := strings.CutPrefix(strings.TrimSpace(line), "XDG_DOCUMENTS_DIR=")
				if !ok {
					continue
				}
				v = strings.Trim(v, `"`)
				v = strings.Replace(v, "$HOME", home, 1)
				if filepath.IsAbs(v) && v != home {
					return v
				}
			}
		}
	}
	return filepath.Join(home, "Documents")
}

func defaultProjectsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(documentsDir(home), "Ableton", "abcollab")
}

func browseFrom() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	docs := documentsDir(home)
	for _, d := range []string{filepath.Join(docs, "Ableton"), docs} {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d
		}
	}
	return home
}

// DefaultSet: szablon nowego seta (File > New w Live).
func (b *backend) DefaultSet() string { return live.DefaultSet() }

// OpenInLive otwiera set w Live (skojarzenie .als w systemie; na Linuksie
// launcher Live'a pod Wine z ableton-linux albo xdg-open).
func (b *backend) OpenInLive(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		for _, c := range []string{"ableton-live", "xdg-open"} {
			if p, err := exec.LookPath(c); err == nil {
				cmd = exec.Command(p, path)
				break
			}
		}
	}
	if cmd == nil {
		return fmt.Errorf("can't start Live from here - open %s in Live yourself", path)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// installLive instaluje/aktualizuje skrypt AbletonCollab w Live (User
// Library). Zwraca token ("" - nie ma gdzie zainstalowac).
func installLive() string {
	dir, err := configDir()
	if err != nil {
		return ""
	}
	token, err := live.Token(filepath.Join(dir, "live-token.txt"))
	if err != nil {
		return ""
	}
	dirs, changed, err := live.Install(token)
	switch {
	case err != nil:
		fmt.Fprintln(os.Stderr, "can't install the Live script:", err)
		return ""
	case len(dirs) == 0:
		fmt.Fprintln(os.Stderr, "Ableton Live's User Library not found - live sync is off (set ABCOLLAB_USER_LIBRARY)")
		return ""
	case changed:
		fmt.Fprintln(os.Stderr, "installed the AbletonCollab script for Live - restart Live and pick it in Settings > Link, Tempo & MIDI > Control Surface")
	}
	return token
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
	if !offline {
		be.token = installLive()
	}
	app := tui.New(be, tui.Options{Me: d, AskName: isNew, Last: last, BaseDir: defaultProjectsDir(),
		BrowseFrom: browseFrom(), Offline: offline, JoinCode: code})
	_, err = tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

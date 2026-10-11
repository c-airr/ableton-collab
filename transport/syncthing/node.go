// Package syncthing to transport abcollab: dolaczony Syncthing uruchamiany
// jako proces poboczny i sterowany przez REST API.
//
// Syncthing robi cala robote sieciowa: znajduje urzadzenia przez publiczne
// discovery, laczy bezposrednio (LAN, hole punching) albo przez publiczne
// relaye, gdy obie strony sa za CGNAT. Dane sa szyfrowane miedzy urzadzeniami.
// My tylko konfigurujemy foldery projektow i urzadzenia (parowanie).
package syncthing

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Options struct {
	Home string // katalog Syncthinga (klucze, konfiguracja, baza)
	Exe  string
	Name string // nazwa tego komputera widoczna u innych
	// LocalOnly: tylko 127.0.0.1, bez discovery i relayow (testy na jednej maszynie).
	LocalOnly bool
}

// Node: dzialajacy Syncthing. Jesli przy starcie juz dzialal (np. drugie okno
// abcollab albo proces po awarii), podpinamy sie do niego i go nie zamykamy.
type Node struct {
	*Client
	ID   string
	opts Options
	cmd  *exec.Cmd
	done chan struct{}
}

// state: to, czego potrzebujemy, zeby znalezc nasz Syncthing ponownie.
type state struct {
	GUI    string `json:"gui"`
	APIKey string `json:"apikey"`
	Listen int    `json:"listen"`
}

func (o Options) statePath() string { return filepath.Join(o.Home, "abcollab.json") }

func Start(o Options) (*Node, error) {
	if err := os.MkdirAll(o.Home, 0o700); err != nil {
		return nil, err
	}
	var st state
	if data, err := os.ReadFile(o.statePath()); err == nil {
		json.Unmarshal(data, &st)
	}
	if st.GUI != "" && st.APIKey != "" {
		if c := newClient(st.GUI, st.APIKey); c.Healthy() {
			n := &Node{Client: c, opts: o}
			return n, n.configure(st, false)
		}
	}
	if _, err := os.Stat(filepath.Join(o.Home, "config.xml")); err != nil {
		out, err := exec.Command(o.Exe, "generate", "--home="+o.Home, "--no-port-probing").CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("syncthing generate: %v: %s", err, tail(out))
		}
	}
	if st.APIKey == "" {
		st.APIKey = randHex(16)
	}
	if st.Listen == 0 {
		st.Listen = listenPort(o.LocalOnly)
	}
	gui, err := freePort()
	if err != nil {
		return nil, err
	}
	st.GUI = "http://127.0.0.1:" + strconv.Itoa(gui)
	data, _ := json.MarshalIndent(st, "", "  ")
	if err := os.WriteFile(o.statePath(), data, 0o600); err != nil {
		return nil, err
	}
	n := &Node{Client: newClient(st.GUI, st.APIKey), opts: o}
	if err := n.spawn(); err != nil {
		return nil, err
	}
	if err := n.configure(st, true); err != nil {
		n.Stop()
		return nil, err
	}
	return n, nil
}

func (n *Node) spawn() error {
	o := n.opts
	cmd := exec.Command(o.Exe, "serve", "--home="+o.Home, "--no-browser", "--no-upgrade", "--no-restart",
		"--gui-address="+n.Base, "--gui-apikey="+n.Key, "--log-file="+filepath.Join(o.Home, "syncthing.log"))
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting Syncthing: %w", err)
	}
	n.cmd, n.done = cmd, make(chan struct{})
	go func() { cmd.Wait(); close(n.done) }()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-n.done:
			return fmt.Errorf("Syncthing exited on start - see %s", filepath.Join(o.Home, "syncthing.log"))
		case <-time.After(200 * time.Millisecond):
		}
		if n.Healthy() {
			return nil
		}
	}
	n.kill()
	return errors.New("Syncthing is not responding after 30 s")
}

// configure: nasze ustawienia (bez statystyk, bez auto-aktualizacji, nasze
// porty) i nazwa urzadzenia. Zmiana adresow nasluchu moze wymagac restartu -
// uruchamiamy z --no-restart, wiec restartujemy sami.
func (n *Node) configure(st state, owned bool) error {
	id, err := n.MyID()
	if err != nil {
		return err
	}
	n.ID = id
	p := strconv.Itoa(st.Listen)
	opts := map[string]any{
		"urAccepted":            -1,
		"autoUpgradeIntervalH":  0,
		"startBrowser":          false,
		"crashReportingEnabled": false,
		"globalAnnounceEnabled": !n.opts.LocalOnly,
		"localAnnounceEnabled":  !n.opts.LocalOnly,
		"relaysEnabled":         !n.opts.LocalOnly,
		"natEnabled":            !n.opts.LocalOnly,
		"listenAddresses":       []string{"tcp://0.0.0.0:" + p, "quic://0.0.0.0:" + p, "dynamic+https://relays.syncthing.net/endpoint"},
	}
	if n.opts.LocalOnly {
		opts["listenAddresses"] = []string{"tcp://127.0.0.1:" + p}
		opts["reconnectionIntervalS"] = 2
	}
	if err := n.PatchOptions(opts); err != nil {
		return err
	}
	if n.opts.Name != "" {
		if err := n.PatchDevice(id, map[string]any{"name": n.opts.Name}); err != nil {
			return err
		}
	}
	if restart, err := n.RestartRequired(); err == nil && restart && owned {
		n.Shutdown()
		n.wait(10 * time.Second)
		return n.spawn()
	}
	return nil
}

// DeviceID: ID tego urzadzenia w Syncthingu.
func (n *Node) DeviceID() string { return n.ID }

// Owned: czy to my uruchomilismy proces (i zamkniemy go przy wyjsciu).
func (n *Node) Owned() bool { return n.cmd != nil }

// ListenAddr: adres nasluchu (testy na jednej maszynie podaja go wprost).
func (n *Node) ListenAddr() string {
	var st state
	data, _ := os.ReadFile(n.opts.statePath())
	json.Unmarshal(data, &st)
	return "tcp://127.0.0.1:" + strconv.Itoa(st.Listen)
}

// Stop zamyka Syncthing, jesli to my go uruchomilismy.
func (n *Node) Stop() {
	if n.cmd == nil {
		return
	}
	if n.Shutdown() != nil || !n.wait(15*time.Second) {
		n.kill()
	}
}

func (n *Node) wait(d time.Duration) bool {
	select {
	case <-n.done:
		return true
	case <-time.After(d):
		return false
	}
}

func (n *Node) kill() {
	if n.cmd != nil && n.cmd.Process != nil {
		n.cmd.Process.Kill()
		n.wait(5 * time.Second)
	}
}

// listenPort: 22000 (domyslny port Syncthinga), chyba ze zajety - np. przez
// osobna instalacje Syncthinga uzytkownika albo druga tozsamosc w testach.
func listenPort(local bool) int {
	if !local {
		if l, err := net.Listen("tcp", ":22000"); err == nil {
			l.Close()
			return 22000
		}
	}
	p, err := freePort()
	if err != nil {
		return 22000
	}
	return p
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 400 {
		s = "..." + s[len(s)-400:]
	}
	return s
}

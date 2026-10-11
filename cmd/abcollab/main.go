// abcollab - wspolna praca nad projektem Ableton Live.
//
// Historia wersji lezy w folderze projektu (.abcollab/), a miedzy komputerami
// przenosi ja dolaczony Syncthing (pobierany przy pierwszym uruchomieniu).
// Z -offline dziala bez sieci - folder przenosi wtedy dowolny sync.
//
//	abcollab                    interfejs terminalowy (TUI)
//	abcollab join <kod>         dolacz do projektu kodem zaproszenia
//	abcollab invite             wypisz kod zaproszenia do projektu
//	abcollab init <set.als>     zaloz projekt w biezacym folderze
//	abcollab status             moja wersja, niezapisane zmiany, inni
//	abcollab commit             zapisz plik seta jako nowa wersje
//	abcollab pull [-i] [-r ..]  pobierz i scal zmiany innych
//	abcollab log [-n N]         historia
//	abcollab whoami [-name X]   tozsamosc tego komputera
//
// Kody wyjscia: 0 ok, 1 nierozwiazany konflikt, 2 blad.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/c-airr/ableton-collab/als"
	"github.com/c-airr/ableton-collab/internal/tui"
	"github.com/c-airr/ableton-collab/store"
	"github.com/c-airr/ableton-collab/transport/syncthing"

	tea "github.com/charmbracelet/bubbletea"
)

func usage() {
	fmt.Fprintln(os.Stderr, `uzycie: abcollab [komenda] [-C folder] [opcje]

  (bez komendy)       interfejs terminalowy (TUI); -offline: bez sieci
  join <kod>          dolacz do projektu kodem zaproszenia
  invite              wypisz kod zaproszenia (prosby przyjmujesz w TUI)
  init <set.als>      zaloz projekt w folderze z setem
  status              moja wersja, niezapisane zmiany, inni uczestnicy
  commit              zapisz aktualny set jako nowa wersje
  pull [-i] [-r ID=ours|theirs|both]
                      pobierz zmiany innych i scal (-i: pytaj przy konflikcie)
  log [-n N]          historia wersji
  whoami [-name X]    tozsamosc tego komputera`)
}

func main() {
	cmd, args := "tui", []string{}
	if len(os.Args) >= 2 {
		cmd, args = os.Args[1], os.Args[2:]
	}
	if strings.HasPrefix(cmd, "-") && cmd != "-h" && cmd != "--help" {
		cmd, args = "tui", os.Args[1:] // abcollab -C folder
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dir := fs.String("C", ".", "folder projektu")
	var err error
	switch cmd {
	case "tui":
		offline := fs.Bool("offline", false, "bez sieci (bez Syncthinga)")
		fs.Parse(args)
		err = runTUI(*dir, *offline, "")
	case "join":
		var code string
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			code, args = args[0], args[1:]
		}
		fs.Parse(args)
		if code == "" {
			code = strings.Join(fs.Args(), "")
		}
		err = runTUI("", false, code)
	case "invite":
		err = withProject(fs, dir, args, cmdInvite)
	case "init":
		err = cmdInit(fs, dir, args)
	case "status":
		err = withProject(fs, dir, args, cmdStatus)
	case "commit":
		err = withProject(fs, dir, args, cmdCommit)
	case "pull":
		interactive := fs.Bool("i", false, "pytaj o kazdy konflikt")
		var rs multi
		fs.Var(&rs, "r", "rozstrzygniecie: ID=ours|theirs|both (wiele razy)")
		err = withProject(fs, dir, args, func(p *store.Project) error { return cmdPull(p, *interactive, rs) })
	case "log":
		n := fs.Int("n", 20, "ile wersji")
		err = withProject(fs, dir, args, func(p *store.Project) error { return cmdLog(p, *n) })
	case "whoami":
		name := fs.String("name", "", "zmien nazwe tego komputera")
		fs.Parse(args)
		err = cmdWhoami(*name)
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if errors.Is(err, errConflict) {
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "blad:", err)
		os.Exit(2)
	}
}

var errConflict = errors.New("konflikt")

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(s string) error { *m = append(*m, s); return nil }

// --- tozsamosc urzadzenia ----------------------------------------------------------

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
	dir, err := configDir()
	if err != nil {
		return store.Device{}, err
	}
	path := filepath.Join(dir, "device.json")
	var d store.Device
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &d); err != nil || d.ID == "" {
			return d, fmt.Errorf("%s uszkodzony", path)
		}
		return d, nil
	}
	name, _ := os.Hostname()
	if name == "" {
		name = "komputer"
	}
	d = store.NewDevice(name)
	return d, saveDevice(path, d)
}

func saveDevice(path string, d store.Device) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(d, "", "  ")
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func cmdWhoami(name string) error {
	d, err := device()
	if err != nil {
		return err
	}
	if name != "" {
		d.Name = name
		dir, _ := configDir()
		if err := saveDevice(filepath.Join(dir, "device.json"), d); err != nil {
			return err
		}
	}
	fmt.Printf("%s (%s)\n", d.Name, d.ID)
	return nil
}

// --- siec i TUI -------------------------------------------------------------------

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
	fmt.Fprintln(os.Stderr, "uruchamianie Syncthinga...")
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

// share udostepnia projekt w Syncthingu i oglasza moje ID transportu w projekcie.
func share(n *syncthing.Node, p *store.Project) (*syncthing.Project, error) {
	sh, err := n.Share(p.Dir, syncthing.FolderID(p.Meta.ID), strings.TrimSuffix(p.Meta.Set, filepath.Ext(p.Meta.Set)))
	if err != nil {
		return nil, err
	}
	p.Me.Peer = n.ID
	return sh, p.Announce()
}

// defaultProjectsDir: tu powstaja projekty, do ktorych dolaczamy.
func defaultProjectsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "Documents", "Ableton", "abcollab")
}

func runTUI(dir string, offline bool, code string) error {
	d, err := device()
	if err != nil {
		return err
	}
	var node *syncthing.Node
	if !offline {
		if node, err = startNode(&d); err != nil {
			return fmt.Errorf("%w\n(bez sieci: abcollab -offline)", err)
		}
		defer node.Stop()
	}
	if code == "" {
		p, err := store.Open(dir, d)
		if err == nil {
			return runProject(p, node)
		}
		if !errors.Is(err, store.ErrNotProject) || node == nil {
			return err
		}
	}
	res, err := tea.NewProgram(tui.NewStart(joiner{node}, d, defaultProjectsDir(), code), tea.WithAltScreen()).Run()
	if err != nil {
		return err
	}
	chosen := res.(tui.Start).Dir()
	if chosen == "" {
		return nil
	}
	p, err := store.Open(chosen, d)
	if err != nil {
		return err
	}
	return runProject(p, node)
}

// joiner: Node dla ekranu startowego (projekt zwracany jako tui.Network).
type joiner struct{ *syncthing.Node }

func (j joiner) Join(inv syncthing.Invite, dir string) (tui.Network, error) {
	p, err := j.Node.Join(inv, dir)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (j joiner) Share(dir, folderID, label string) (tui.Network, error) {
	p, err := j.Node.Share(dir, folderID, label)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func runProject(p *store.Project, node *syncthing.Node) error {
	var net tui.Network // nil bez sieci (nie *syncthing.Project(nil) - to nie byloby nil)
	if node != nil {
		sh, err := share(node, p)
		if err != nil {
			return err
		}
		net = sh
	}
	_, err := tea.NewProgram(tui.New(p, net), tea.WithAltScreen()).Run()
	return err
}

func cmdInvite(p *store.Project) error {
	node, err := startNode(&p.Me)
	if err != nil {
		return err
	}
	defer node.Stop()
	sh, err := share(node, p)
	if err != nil {
		return err
	}
	fmt.Println(sh.Invite(p.Me.Name))
	fmt.Fprintln(os.Stderr, "\nWyslij ten kod osobie, ktora zapraszasz. Ona robi: abcollab join <kod>")
	fmt.Fprintln(os.Stderr, "Prosbe przyjmujesz w abcollab (zakladka Lobby) - musi byc wtedy wlaczony.")
	fmt.Fprintf(os.Stderr, "Twoj kod urzadzenia: %s\n", syncthing.ShortID(node.ID))
	return nil
}

// --- komendy -----------------------------------------------------------------------

func withProject(fs *flag.FlagSet, dir *string, args []string, fn func(*store.Project) error) error {
	fs.Parse(args)
	d, err := device()
	if err != nil {
		return err
	}
	p, err := store.Open(*dir, d)
	if err != nil {
		return err
	}
	return fn(p)
}

func cmdInit(fs *flag.FlagSet, dir *string, args []string) error {
	// set moze byc przed albo po flagach
	var set string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		set, args = args[0], args[1:]
	}
	fs.Parse(args)
	if set == "" && fs.NArg() > 0 {
		set = fs.Arg(0)
	}
	if set == "" {
		return fmt.Errorf("podaj plik seta: abcollab init <set.als>")
	}
	d, err := device()
	if err != nil {
		return err
	}
	p, v, err := store.Init(*dir, filepath.Base(set), d)
	if err != nil {
		return err
	}
	fmt.Printf("projekt zalozony: %s, set %s, wersja %s\n", p.Dir, p.Meta.Set, v.Short())
	fmt.Println("uruchom abcollab i zapros wspolpracownikow w zakladce Lobby (i)")
	if ws := assetWarnings(p); len(ws) > 0 {
		for _, w := range ws {
			fmt.Println(w)
		}
	}
	return nil
}

func assetWarnings(p *store.Project) []als.Warning {
	doc, err := als.ReadFile(p.WorkingPath())
	if err != nil {
		return nil
	}
	return als.AssetWarnings(doc, p.Dir)
}

func cmdStatus(p *store.Project) error {
	st, err := p.Status()
	if err != nil {
		return err
	}
	fmt.Printf("projekt: %s (set %s)\nja: %s\n", p.Dir, p.Meta.Set, p.Me.Name)
	if st.Head == nil {
		fmt.Println("wersja: brak - zrob `abcollab pull`, zeby pobrac projekt")
	} else {
		fmt.Printf("wersja: %s (%s, %s)\n", st.Head.Short(), st.Head.Author, st.Head.Time.Local().Format("2006-01-02 15:04"))
	}
	if len(st.Changes) > 0 {
		fmt.Printf("\nniezapisane w historii zmiany (%d) - `abcollab commit`:\n", len(st.Changes))
		for i, c := range st.Changes {
			if i == 10 {
				fmt.Printf("  ... i %d wiecej\n", len(st.Changes)-10)
				break
			}
			fmt.Println("  " + c.String())
		}
	}
	fmt.Println()
	if len(st.Peers) == 0 {
		fmt.Println("inni: nikt jeszcze nie dolaczyl")
	}
	for _, ps := range st.Peers {
		line := fmt.Sprintf("  %-20s %s", ps.Device.Name, ps.Relation)
		if ps.Head != nil {
			line += fmt.Sprintf("  [%s, %s]", ps.Head.Short(), ps.Head.Time.Local().Format("2006-01-02 15:04"))
		}
		fmt.Println(line)
	}
	return nil
}

func cmdCommit(p *store.Project) error {
	if p.Head() == "" {
		return fmt.Errorf("najpierw `abcollab pull` - nie masz jeszcze wersji tego projektu")
	}
	v, err := p.Commit()
	if err != nil {
		return err
	}
	if v == nil {
		fmt.Println("brak zmian")
		return nil
	}
	fmt.Printf("wersja %s:\n", v.Short())
	for _, s := range v.Summary {
		fmt.Println("  " + s)
	}
	return nil
}

func cmdPull(p *store.Project, interactive bool, rs multi) error {
	res := map[string]als.Resolution{}
	for _, r := range rs {
		id, choice, ok := strings.Cut(r, "=")
		if !ok {
			return fmt.Errorf("-r %q: oczekiwano ID=wybor", r)
		}
		c, err := als.ParseResolution(choice)
		if err != nil {
			return err
		}
		res[id] = c
	}
	in := bufio.NewScanner(os.Stdin)
	for {
		r, err := p.Pull(res)
		if err != nil {
			return err
		}
		report(p, r)
		if len(r.Conflicts) == 0 {
			return nil
		}
		if !interactive {
			fmt.Fprintf(os.Stderr, "\n%d konflikt(y) z %s - nic dla tej osoby nie zostalo zapisane\n\n", len(r.Conflicts), r.ConflictWith.Name)
			for _, c := range r.Conflicts {
				fmt.Fprint(os.Stderr, c)
				fmt.Fprintf(os.Stderr, "  rozstrzygniecie: -r '%s=%s'\n\n", c.ID, choices(c))
			}
			fmt.Fprintln(os.Stderr, "uruchom `abcollab pull -i`, zeby wybrac interaktywnie")
			return errConflict
		}
		for _, c := range r.Conflicts {
			fmt.Fprintf(os.Stderr, "\nkonflikt z %s\n", r.ConflictWith.Name)
			fmt.Fprint(os.Stderr, c)
			res[c.ID] = ask(in, c)
		}
	}
}

func choices(c als.Conflict) string {
	if c.CanBoth {
		return "ours|theirs|both"
	}
	return "ours|theirs"
}

func ask(in *bufio.Scanner, c als.Conflict) als.Resolution {
	prompt := "  [o] zachowaj moje  [t] zachowaj ich"
	if c.CanBoth {
		prompt += "  [b] zachowaj oba (klip ich do wolnego slotu)"
	}
	for {
		fmt.Fprint(os.Stderr, prompt+": ")
		if !in.Scan() {
			fmt.Fprintln(os.Stderr, "\nprzerwano")
			os.Exit(1)
		}
		r, err := als.ParseResolution(in.Text())
		if err == nil && (r != als.Both || c.CanBoth) {
			return r
		}
	}
}

func report(p *store.Project, r *store.PullResult) {
	if r.Committed != nil {
		fmt.Printf("zapisano twoje zmiany jako wersje %s\n", r.Committed.Short())
	}
	if r.Backup != "" {
		fmt.Printf("twoj dotychczasowy set skopiowany do: %s\n", r.Backup)
	}
	for _, d := range r.FastForwarded {
		fmt.Printf("przyjeto zmiany od %s\n", d.Name)
	}
	for _, v := range r.Merged {
		fmt.Printf("scalono (wersja %s):\n", v.Short())
		for _, s := range v.Summary {
			fmt.Println("  " + s)
		}
	}
	for _, w := range r.Warnings {
		fmt.Println(w)
	}
	for _, d := range r.Incomplete {
		fmt.Printf("zmiany od %s jeszcze sie synchronizuja - sprobuj za chwile\n", d.Name)
	}
	if r.WorkingChanged() {
		fmt.Printf("\n%s zostal zaktualizowany. Jesli masz go otwartego w Live, otworz go ponownie\n", p.Meta.Set)
		fmt.Println("(File > Open Recent) i NIE zapisuj starej wersji - nadpisalbys zmiany innych.")
	} else if len(r.Conflicts) == 0 && len(r.Incomplete) == 0 && r.Committed == nil {
		fmt.Println("wszystko aktualne")
	}
}

func cmdLog(p *store.Project, n int) error {
	vs, err := p.Versions()
	if err != nil {
		return err
	}
	head := p.Head()
	for i, v := range store.Log(vs) {
		if i == n {
			break
		}
		mark := " "
		if v.ID == head {
			mark = "*"
		}
		kind := ""
		if len(v.Parents) > 1 {
			kind = " [scalenie]"
		}
		fmt.Printf("%s %s  %s  %s%s\n", mark, v.Short(), v.Time.Local().Format("2006-01-02 15:04"), v.Author, kind)
		for j, s := range v.Summary {
			if j == 3 {
				fmt.Printf("      ... (%d linii)\n", len(v.Summary))
				break
			}
			if len(s) > 120 {
				s = s[:120] + "..."
			}
			fmt.Println("      " + s)
		}
	}
	return nil
}

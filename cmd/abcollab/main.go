// abcollab - work on one Ableton Live project together.
//
// Historia wersji lezy w folderze sesji (.abcollab/), a miedzy komputerami
// przenosi ja dolaczony Syncthing (pobierany przy pierwszym uruchomieniu).
// Z -offline dziala bez sieci - folder przenosi wtedy dowolny sync.
//
//	abcollab                    panel (ostatnia sesja, Join/Host)
//	abcollab join <code>        panel z otwartym dolaczaniem
//	abcollab invite             wypisz kod zaproszenia
//	abcollab init <set.als>     zaloz sesje w folderze
//	abcollab status|commit|pull|log|whoami
//
// Kody wyjscia: 0 ok, 1 nierozwiazany konflikt, 2 blad.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/c-airr/ableton-collab/als"
	"github.com/c-airr/ableton-collab/store"
	"github.com/c-airr/ableton-collab/transport/syncthing"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage: abcollab [command] [-C folder] [options]

  (no command)        the panel: your last session, Join / Host
                      -C folder: open this session; -offline: no network
  join <code>         open the panel with joining started
  invite              print an invite code (accept requests in the panel)
  init <set.als>      make the folder with this set a session
  status              my version, unsaved changes, other people
  commit              save the current set as a new version
  pull [-i] [-r ID=ours|theirs|both]
                      get others' changes and merge (-i: ask on conflicts)
  log [-n N]          version history
  whoami [-name X]    this computer's identity
  version             program version`)
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
	dir := fs.String("C", ".", "session folder")
	var err error
	switch cmd {
	case "tui":
		offline := fs.Bool("offline", false, "no network (no Syncthing)")
		fs.Parse(args)
		open := ""
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "C" {
				open = *dir
			}
		})
		if *offline && open == "" {
			open = *dir
		}
		if open != "" {
			if abs, err := filepath.Abs(open); err == nil {
				open = abs
			}
		}
		err = runTUI(open, *offline, "")
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
		interactive := fs.Bool("i", false, "ask about every conflict")
		var rs multi
		fs.Var(&rs, "r", "resolution: ID=ours|theirs|both (repeatable)")
		err = withProject(fs, dir, args, func(p *store.Project) error { return cmdPull(p, *interactive, rs) })
	case "log":
		n := fs.Int("n", 20, "how many versions")
		err = withProject(fs, dir, args, func(p *store.Project) error { return cmdLog(p, *n) })
	case "whoami":
		name := fs.String("name", "", "change this computer's nickname")
		fs.Parse(args)
		err = cmdWhoami(*name)
	case "version", "--version", "-version":
		fmt.Println("abcollab", version, "(Syncthing "+syncthing.Version+")")
		return
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
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
}

var errConflict = errors.New("conflict")

// version: ustawiane przy budowaniu wydania (-ldflags "-X main.version=v0.1.0").
var version = "dev"

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(s string) error { *m = append(*m, s); return nil }

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
	fmt.Fprintln(os.Stderr, "\nSend this code to the person you're inviting - they press Join in abcollab and paste it.")
	fmt.Fprintln(os.Stderr, "You accept their request in the abcollab panel (it has to be open then).")
	fmt.Fprintf(os.Stderr, "Your device code: %s\n", syncthing.ShortID(node.ID))
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
		return fmt.Errorf("give the set file: abcollab init <set.als>")
	}
	d, err := device()
	if err != nil {
		return err
	}
	p, v, err := store.Init(*dir, filepath.Base(set), d)
	if err != nil {
		return err
	}
	fmt.Printf("session created: %s, set %s, version %s\n", p.Dir, p.Meta.Set, v.Short())
	fmt.Println("run abcollab and press Invite to bring people in")
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
	fmt.Printf("session: %s (set %s)\nme: %s\n", p.Dir, p.Meta.Set, p.Me.Name)
	if st.Head == nil {
		fmt.Println("version: none - run `abcollab pull` to get the session")
	} else {
		fmt.Printf("version: %s (%s, %s)\n", st.Head.Short(), st.Head.Author, st.Head.Time.Local().Format("2006-01-02 15:04"))
	}
	if len(st.Changes) > 0 {
		fmt.Printf("\nunsaved changes (%d) - `abcollab commit`:\n", len(st.Changes))
		for i, c := range st.Changes {
			if i == 10 {
				fmt.Printf("  ... and %d more\n", len(st.Changes)-10)
				break
			}
			fmt.Println("  " + c.String())
		}
	}
	fmt.Println()
	if len(st.Peers) == 0 {
		fmt.Println("others: nobody joined yet")
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
		return fmt.Errorf("run `abcollab pull` first - you don't have a version of this session yet")
	}
	v, err := p.Commit()
	if err != nil {
		return err
	}
	if v == nil {
		fmt.Println("no changes")
		return nil
	}
	fmt.Printf("version %s:\n", v.Short())
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
			return fmt.Errorf("-r %q: expected ID=choice", r)
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
			fmt.Fprintf(os.Stderr, "\n%d conflict(s) with %s - nothing from them was saved\n\n", len(r.Conflicts), r.ConflictWith.Name)
			for _, c := range r.Conflicts {
				fmt.Fprint(os.Stderr, c)
				fmt.Fprintf(os.Stderr, "  resolve with: -r '%s=%s'\n\n", c.ID, choices(c))
			}
			fmt.Fprintln(os.Stderr, "run `abcollab pull -i` to choose interactively")
			return errConflict
		}
		for _, c := range r.Conflicts {
			fmt.Fprintf(os.Stderr, "\nconflict with %s\n", r.ConflictWith.Name)
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
	prompt := "  [o] keep mine  [t] keep theirs"
	if c.CanBoth {
		prompt += "  [b] keep both (their clip to a free slot)"
	}
	for {
		fmt.Fprint(os.Stderr, prompt+": ")
		if !in.Scan() {
			fmt.Fprintln(os.Stderr, "\ncancelled")
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
		fmt.Printf("saved your changes as version %s\n", r.Committed.Short())
	}
	if r.Backup != "" {
		fmt.Printf("your previous set was copied to: %s\n", r.Backup)
	}
	for _, d := range r.FastForwarded {
		fmt.Printf("got changes from %s\n", d.Name)
	}
	for _, v := range r.Merged {
		fmt.Printf("merged (version %s):\n", v.Short())
		for _, s := range v.Summary {
			fmt.Println("  " + s)
		}
	}
	for _, w := range r.Warnings {
		fmt.Println(w)
	}
	for _, d := range r.Incomplete {
		fmt.Printf("changes from %s are still arriving - try again in a moment\n", d.Name)
	}
	if r.WorkingChanged() {
		fmt.Printf("\n%s was updated. If it's open in Live, reopen it\n", p.SetName())
		fmt.Println("(File > Open Recent) and DON'T save the old one - you'd overwrite the others' changes.")
	} else if len(r.Conflicts) == 0 && len(r.Incomplete) == 0 && r.Committed == nil {
		fmt.Println("everything is up to date")
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
			kind = " [merge]"
		}
		fmt.Printf("%s %s  %s  %s%s\n", mark, v.Short(), v.Time.Local().Format("2006-01-02 15:04"), v.Author, kind)
		for j, s := range v.Summary {
			if j == 3 {
				fmt.Printf("      ... (%d lines)\n", len(v.Summary))
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

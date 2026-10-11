package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/c-airr/ableton-collab/store"
	"github.com/c-airr/ableton-collab/transport/syncthing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Backend: to, czego menu potrzebuje od reszty programu (w main: Syncthing
// + store). Projekty wracaja jako Network, zeby menu dalo sie testowac.
type Backend interface {
	DeviceID() string
	Folders() ([]syncthing.FolderConfig, error)
	Join(inv syncthing.Invite, dir string) (Network, error)
	Share(dir, folderID, label string) (Network, error)
	// Create zaklada projekt z setu Live i udostepnia go; zwraca folder projektu.
	Create(set string) (string, error)
	Rename(name string) error
}

type startStage int

const (
	stName startStage = iota // pierwsze uruchomienie: jak cie widza inni
	stMenu                   // projekty + nowy + dolacz
	stNew                    // wybor setu na nowy projekt
	stCode                   // wklejanie kodu
	stDir                    // gdzie zalozyc folder
	stWait                   // czekanie na akceptacje i pobranie
)

// Start: menu glowne. Po zakonczeniu Dir() to folder projektu do otwarcia
// ("" - wyjscie), a Created() mowi, czy projekt wlasnie powstal (wtedy
// od razu pokazujemy zaproszenie).
type Start struct {
	be      Backend
	me      store.Device
	baseDir string   // domyslne miejsce na projekty, do ktorych dolaczamy
	roots   []string // gdzie szukac setow Live na nowy projekt
	here    string   // projekt w biezacym folderze (jesli jest)

	stage    startStage
	projects []syncthing.FolderConfig
	sets     []string
	cur      int
	input    string
	inv      syncthing.Invite
	err      error
	waitMsg  string
	share    Network
	dir      string
	done     bool
	created  bool
	width    int
	pollWait time.Duration
}

type StartOptions struct {
	Me      store.Device
	AskName bool     // pierwsze uruchomienie
	BaseDir string   // folder na projekty, do ktorych sie dolacza
	Roots   []string // gdzie szukac setow Live
	Here    string   // projekt w biezacym folderze
	Code    string   // kod z `abcollab join <kod>`
}

func NewStart(be Backend, o StartOptions) Start {
	s := Start{be: be, me: o.Me, baseDir: o.BaseDir, roots: o.Roots, here: o.Here, width: 100, pollWait: time.Second, stage: stMenu}
	switch {
	case o.Code != "":
		// kod z `abcollab join <kod>`: poprawny - od razu wybor folderu
		s.stage, s.input = stCode, o.Code
		if inv, err := syncthing.ParseInvite(o.Code); err == nil {
			s.inv, s.stage, s.input = inv, stDir, filepath.Join(o.BaseDir, inv.Name+" Project")
		} else {
			s.err = err
		}
	case o.AskName:
		s.stage, s.input = stName, o.Me.Name
	}
	return s
}

// Dir: wybrany, zalozony albo pobrany folder projektu ("" - wyjscie).
func (s Start) Dir() string {
	if s.done {
		return s.dir
	}
	return ""
}

func (s Start) Created() bool    { return s.done && s.created }
func (s Start) Me() store.Device { return s.me }

type projectsMsg struct {
	list []syncthing.FolderConfig
	err  error
}

type setsMsg []string

type joinedMsg struct {
	share Network
	err   error
}

type createdMsg struct {
	dir string
	err error
}

type waitMsg struct {
	text  string
	ready bool
	err   error
}

func (s Start) Init() tea.Cmd { return s.loadProjects() }

func (s Start) loadProjects() tea.Cmd {
	be, here := s.be, s.here
	return func() tea.Msg {
		fs, err := be.Folders()
		var out []syncthing.FolderConfig
		for _, f := range fs {
			if strings.HasPrefix(f.ID, "abcollab-") {
				out = append(out, f)
			}
		}
		sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
		if here != "" {
			known := false
			for _, f := range out {
				known = known || samePath(f.Path, here)
			}
			if !known {
				out = append([]syncthing.FolderConfig{{Label: filepath.Base(here), Path: here}}, out...)
			}
		}
		return projectsMsg{out, err}
	}
}

// menu: projekty, potem stale pozycje.
const (
	miNew = iota
	miJoin
	miQuit
	menuFixed
)

func (s Start) menuLen() int { return len(s.projects) + menuFixed }

func (s Start) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width = msg.Width
	case projectsMsg:
		s.projects = msg.list
		if msg.err != nil {
			s.err = msg.err
		}
	case setsMsg:
		s.sets = msg
	case joinedMsg:
		if msg.err != nil {
			s.stage, s.err = stDir, msg.err
			return s, nil
		}
		s.share, s.stage = msg.share, stWait
		return s, s.poll()
	case createdMsg:
		if msg.err != nil {
			s.err = msg.err
			return s, nil
		}
		s.dir, s.done, s.created = msg.dir, true, true
		return s, tea.Quit
	case waitMsg:
		if msg.ready {
			s.done = true
			return s, tea.Quit
		}
		s.waitMsg, s.err = msg.text, msg.err
		return s, tea.Tick(s.pollWait, func(time.Time) tea.Msg { return s.poll()() })
	case tea.KeyMsg:
		return s.key(msg)
	}
	return s, nil
}

func (s Start) toMenu() Start {
	s.stage, s.input, s.err, s.cur = stMenu, "", nil, 0
	return s
}

func (s Start) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		return s, tea.Quit
	}
	if k.String() == "esc" {
		switch s.stage {
		case stMenu, stName:
			if s.stage == stName {
				return s.toMenu(), nil
			}
			return s, tea.Quit
		case stDir:
			s.stage, s.input, s.err = stCode, s.inv.Code(), nil
			return s, nil
		case stWait:
			return s, tea.Quit
		}
		return s.toMenu(), s.loadProjects()
	}
	switch s.stage {
	case stMenu:
		switch k.String() {
		case "q":
			return s, tea.Quit
		case "up", "k":
			if s.cur > 0 {
				s.cur--
			}
		case "down", "j", "tab":
			if s.cur < s.menuLen()-1 {
				s.cur++
			}
		case "n":
			return s.choose(len(s.projects) + miNew)
		case "d":
			return s.choose(len(s.projects) + miJoin)
		case "enter":
			return s.choose(s.cur)
		}
		return s, nil
	case stNew:
		switch k.Type {
		case tea.KeyUp:
			if s.cur > 0 {
				s.cur--
			}
			return s, nil
		case tea.KeyDown:
			if s.cur < len(s.sets)-1 {
				s.cur++
			}
			return s, nil
		}
	}
	if s.stage == stWait {
		return s, nil
	}
	switch k.Type {
	case tea.KeyEnter:
		return s.submit()
	case tea.KeyBackspace:
		if r := []rune(s.input); len(r) > 0 {
			s.input = string(r[:len(r)-1])
		}
	case tea.KeyCtrlU:
		s.input = ""
	case tea.KeySpace:
		s.input += " "
	case tea.KeyRunes:
		s.input += string(k.Runes)
	}
	return s, nil
}

func (s Start) choose(i int) (tea.Model, tea.Cmd) {
	s.err = nil
	if i < len(s.projects) {
		return s.open(s.projects[i])
	}
	switch i - len(s.projects) {
	case miNew:
		s.stage, s.input, s.cur, s.sets = stNew, "", 0, nil
		roots := s.roots
		return s, func() tea.Msg { return setsMsg(FindSets(roots, 15)) }
	case miJoin:
		s.stage, s.input = stCode, ""
	case miQuit:
		return s, tea.Quit
	}
	return s, nil
}

func (s Start) open(f syncthing.FolderConfig) (tea.Model, tea.Cmd) {
	if _, err := store.Open(f.Path, s.me); errors.Is(err, store.ErrNotProject) && f.ID != "" {
		// dolaczanie przerwane wczesniej - projekt jeszcze nie doszedl
		sh, err := s.be.Share(f.Path, f.ID, f.Label)
		if err != nil {
			s.err = err
			return s, nil
		}
		s.share, s.dir, s.stage = sh, f.Path, stWait
		s.inv = syncthing.Invite{Name: f.Label, Host: "zapraszajacy"}
		return s, s.poll()
	} else if err != nil {
		s.err = err
		return s, nil
	}
	s.dir, s.done = f.Path, true
	return s, tea.Quit
}

func (s Start) submit() (tea.Model, tea.Cmd) {
	switch s.stage {
	case stName:
		name := strings.TrimSpace(s.input)
		if name == "" {
			return s, nil
		}
		if err := s.be.Rename(name); err != nil {
			s.err = err
			return s, nil
		}
		s.me.Name = name
		return s.toMenu(), nil
	case stNew:
		set := cleanPath(s.input)
		if set == "" && s.cur < len(s.sets) {
			set = s.sets[s.cur]
		}
		if set == "" {
			return s, nil
		}
		be := s.be
		return s, func() tea.Msg {
			dir, err := be.Create(set)
			return createdMsg{dir, err}
		}
	case stCode:
		inv, err := syncthing.ParseInvite(s.input)
		if err != nil {
			s.err = err
			return s, nil
		}
		s.inv, s.err, s.stage = inv, nil, stDir
		s.input = filepath.Join(s.baseDir, inv.Name+" Project")
	case stDir:
		dir := cleanPath(s.input)
		if dir == "" {
			return s, nil
		}
		s.dir, s.err = dir, nil
		be, inv := s.be, s.inv
		return s, func() tea.Msg {
			sh, err := be.Join(inv, dir)
			return joinedMsg{sh, err}
		}
	}
	return s, nil
}

// cleanPath: sciezka wpisana albo przeciagnieta do terminala (bywa w cudzyslowach).
func cleanPath(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "& ") // PowerShell po przeciagnieciu pliku
	return strings.Trim(s, `"'`)
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	return a == b || os.PathSeparator == '\\' && strings.EqualFold(a, b)
}

// poll: czy zapraszajacy juz nas przyjal i czy projekt doszedl w calosci;
// gdy tak - pobranie (checkout) seta i koniec.
func (s Start) poll() tea.Cmd {
	share, dir, me, host := s.share, s.dir, s.me, s.inv.Host
	return func() tea.Msg {
		connected := false
		if l, err := share.Lobby(); err == nil {
			for _, m := range l.Members {
				connected = connected || m.Connected
			}
		}
		p, err := store.Open(dir, me)
		if errors.Is(err, store.ErrNotProject) {
			if connected {
				return waitMsg{text: "Polaczono z " + host + " - pobieranie projektu..."}
			}
			return waitMsg{text: "Czekam, az " + host + " przyjmie prosbe (musi miec wlaczony abcollab)."}
		}
		if err != nil {
			return waitMsg{err: err}
		}
		r, err := p.Pull(nil)
		if err != nil {
			return waitMsg{err: err}
		}
		if p.Head() == "" || len(r.Incomplete) > 0 {
			return waitMsg{text: "Pobieranie projektu..."}
		}
		return waitMsg{ready: true}
	}
}

// FindSets szuka setow Live (.als w folderze projektu Live, czyli obok
// "Ableton Project Info"), ktore nie sa jeszcze projektami abcollab.
// Najnowsze pierwsze. Plytko i z limitem - User Library bywa ogromna.
func FindSets(roots []string, limit int) []string {
	type found struct {
		path string
		mod  time.Time
	}
	var out []found
	seen := map[string]bool{}
	budget := 20000
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		ents, err := os.ReadDir(dir)
		if err != nil || budget <= 0 {
			return
		}
		budget -= len(ents)
		isProject, isOurs := false, false
		for _, e := range ents {
			isProject = isProject || e.IsDir() && e.Name() == "Ableton Project Info"
			isOurs = isOurs || e.Name() == store.Dir
		}
		for _, e := range ents {
			name := e.Name()
			if e.IsDir() {
				if depth < 4 && name != "Backup" && name != "User Library" && name != "Factory Packs" && name != "Samples" && !strings.HasPrefix(name, ".") {
					walk(filepath.Join(dir, name), depth+1)
				}
				continue
			}
			if isProject && !isOurs && strings.EqualFold(filepath.Ext(name), ".als") {
				p := filepath.Join(dir, name)
				if fi, err := e.Info(); err == nil && !seen[p] {
					seen[p] = true
					out = append(out, found{p, fi.ModTime()})
				}
			}
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mod.After(out[j].mod) })
	var paths []string
	for i, f := range out {
		if i == limit {
			break
		}
		paths = append(paths, f.path)
	}
	return paths
}

// --- widok -----------------------------------------------------------------------------

func (s Start) View() string {
	w := max(40, s.width)
	var b strings.Builder
	b.WriteString(sTitle.Render("ableton-collab") + sDim.Render("  ·  "+s.me.Name) + "\n\n")
	switch s.stage {
	case stName:
		b.WriteString(sBold.Render("Witaj!") + " Jak maja cie widziec inni uczestnicy?\n\n")
		b.WriteString(inputBox(s.input, w) + "\n")
		b.WriteString(keys("enter", "dalej", "ctrl+u", "wyczysc"))
	case stMenu:
		if len(s.projects) > 0 {
			b.WriteString(sBold.Render("Twoje projekty") + "\n")
			for i, p := range s.projects {
				line := fmt.Sprintf("%-26s %s", trunc(p.Label, 26), sDim.Render(trunc(p.Path, w-34)))
				b.WriteString(cursor(i == s.cur) + line + "\n")
			}
			b.WriteString("\n")
		}
		n := len(s.projects)
		b.WriteString(cursor(s.cur == n+miNew) + sKey.Render("n") + "  Nowy projekt" + sDim.Render(" - udostepnij swoj set Live i zapros innych") + "\n")
		b.WriteString(cursor(s.cur == n+miJoin) + sKey.Render("d") + "  Dolacz do projektu" + sDim.Render(" - masz kod zaproszenia") + "\n")
		b.WriteString(cursor(s.cur == n+miQuit) + sKey.Render("q") + "  Wyjscie\n")
		b.WriteString("\n" + keys("↑↓", "wybor", "enter", "otworz"))
	case stNew:
		b.WriteString(sBold.Render("Nowy projekt") + "\n")
		b.WriteString(sDim.Render("Wybierz set (projekt zapisany w Live) albo wpisz / przeciagnij sciezke do pliku .als.") + "\n")
		b.WriteString(sDim.Render("Udostepniany jest caly folder projektu Live (sample w nim tez).") + "\n\n")
		if s.sets == nil {
			b.WriteString(sDim.Render("  szukanie setow...") + "\n")
		} else if len(s.sets) == 0 {
			b.WriteString(sDim.Render("  nie znalazlem setow w folderze Ableton - wpisz sciezke") + "\n")
		}
		for i, p := range s.sets {
			on := i == s.cur && strings.TrimSpace(s.input) == ""
			b.WriteString(cursor(on) + fmt.Sprintf("%-28s %s", trunc(filepath.Base(p), 28), sDim.Render(trunc(filepath.Dir(p), w-36))) + "\n")
		}
		b.WriteString("\n" + inputBox(s.input, w) + "\n")
		b.WriteString(keys("↑↓", "wybor", "enter", "udostepnij", "esc", "menu"))
	case stCode:
		b.WriteString(sBold.Render("Dolacz do projektu") + "\n")
		b.WriteString(sDim.Render("Wklej kod zaproszenia od osoby, ktora zaprasza (zaczyna sie od ABC1).") + "\n\n")
		b.WriteString(inputBox(s.input, w) + "\n")
		b.WriteString(keys("enter", "dalej", "ctrl+u", "wyczysc", "esc", "menu"))
	case stDir:
		b.WriteString(sBold.Render("Projekt "+s.inv.Name) + sDim.Render(" od "+s.inv.Host) + "\n")
		b.WriteString(sDim.Render("Gdzie zapisac projekt? Folder musi byc nowy albo pusty.") + "\n\n")
		b.WriteString(inputBox(s.input, w) + "\n")
		b.WriteString(keys("enter", "dolacz", "esc", "wstecz"))
	case stWait:
		b.WriteString(sBold.Render("Dolaczanie do "+s.inv.Name) + "\n\n")
		b.WriteString("Twoj kod urzadzenia: " + lipgloss.NewStyle().Bold(true).Foreground(cAccent).Render(syncthing.ShortID(s.be.DeviceID())) + "\n")
		b.WriteString(sDim.Render(s.inv.Host+" zobaczy go przy twojej prosbie - podaj mu go, zeby wiedzial, ze to ty.") + "\n\n")
		msg := s.waitMsg
		if msg == "" {
			msg = "Laczenie..."
		}
		b.WriteString(lipgloss.NewStyle().Foreground(cWarn).Render("⟳ "+msg) + "\n")
		b.WriteString(sDim.Render("folder: "+s.dir) + "\n\n")
		b.WriteString(keys("esc", "przerwij (prosba zostaje - wrocisz z menu)"))
	}
	if s.err != nil {
		b.WriteString("\n\n" + sWarnBox.Width(w-4).Render(s.err.Error()))
	}
	return b.String() + "\n"
}

func cursor(on bool) string {
	if on {
		return sKey.Render("› ")
	}
	return "  "
}

func keys(kv ...string) string {
	var parts []string
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, sKey.Render(kv[i])+sDim.Render(" "+kv[i+1]))
	}
	return strings.Join(parts, sDim.Render("  ·  "))
}

func inputBox(s string, w int) string {
	r := []rune(s)
	if max := w - 8; len(r) > max && max > 1 {
		s = "…" + string(r[len(r)-max+1:])
	}
	return sBox.Width(w - 4).Render(s + sKey.Render("▏"))
}

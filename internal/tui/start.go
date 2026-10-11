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
	// Create udostepnia folder sesji z setem (set wzgledem dir, "/"); gdy
	// folder juz jest projektem, set jest pusty. Zwraca folder projektu.
	Create(dir, set string) (string, error)
	Rename(name string) error
}

type startStage int

const (
	stName    startStage = iota // pierwsze uruchomienie: jak cie widza inni
	stMenu                      // projekty + nowy + dolacz
	stBrowse                    // nowy projekt: wybor folderu sesji
	stMkdir                     // nowy folder w przegladarce
	stConfirm                   // folder duzy - na pewno?
	stPickSet                   // kilka setow w folderze
	stWaitSet                   // pusty folder - czekamy, az Live zapisze set
	stCode                      // wklejanie kodu
	stDir                       // gdzie zalozyc folder
	stWait                      // czekanie na akceptacje i pobranie
)

// Start: menu glowne. Po zakonczeniu Dir() to folder projektu do otwarcia
// ("" - wyjscie), a Created() mowi, czy projekt wlasnie powstal (wtedy
// od razu pokazujemy zaproszenie).
type Start struct {
	be         Backend
	me         store.Device
	baseDir    string // domyslne miejsce na projekty, do ktorych dolaczamy
	browseFrom string // gdzie zaczyna przegladarka folderow
	here       string // projekt w biezacym folderze (jesli jest)

	stage    startStage
	projects []syncthing.FolderConfig
	cur      int
	input    string
	br       browser
	info     FolderInfo
	inv      syncthing.Invite
	err      error
	waitMsg  string
	share    Network
	dir      string
	done     bool
	created  bool
	width    int
	height   int
	pollWait time.Duration
}

type StartOptions struct {
	Me         store.Device
	AskName    bool   // pierwsze uruchomienie
	BaseDir    string // folder na projekty, do ktorych sie dolacza
	BrowseFrom string // start przegladarki folderow
	Here       string // projekt w biezacym folderze
	Code       string // kod z `abcollab join <kod>`
}

func NewStart(be Backend, o StartOptions) Start {
	s := Start{be: be, me: o.Me, baseDir: o.BaseDir, browseFrom: o.BrowseFrom, here: o.Here,
		width: 100, height: 30, pollWait: time.Second, stage: stMenu}
	switch {
	case o.Code != "":
		// kod z `abcollab join <kod>`: poprawny - od razu wybor folderu
		s.stage, s.input = stCode, o.Code
		if inv, err := syncthing.ParseInvite(o.Code); err == nil {
			s.inv, s.stage, s.input = inv, stDir, filepath.Join(o.BaseDir, inv.Name)
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

type joinedMsg struct {
	share Network
	err   error
}

type createdMsg struct {
	dir string
	err error
}

type inspectMsg struct {
	info FolderInfo
	err  error
	poll bool // z czekania na set
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

func inspect(dir string, poll bool) tea.Cmd {
	return func() tea.Msg {
		info, err := InspectFolder(dir)
		return inspectMsg{info, err, poll}
	}
}

func (s Start) create(set string) tea.Cmd {
	be, dir := s.be, s.info.Dir
	return func() tea.Msg {
		d, err := be.Create(dir, set)
		return createdMsg{d, err}
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
		s.width, s.height = msg.Width, msg.Height
	case projectsMsg:
		s.projects = msg.list
		if msg.err != nil {
			s.err = msg.err
		}
	case inspectMsg:
		return s.inspected(msg)
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
		if s.stage != stWait {
			return s, nil
		}
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

// inspected: wynik sprawdzenia folderu sesji - co dalej.
func (s Start) inspected(msg inspectMsg) (tea.Model, tea.Cmd) {
	if msg.poll && s.stage != stWaitSet {
		return s, nil // uzytkownik juz wyszedl z czekania
	}
	if msg.err != nil {
		s.stage, s.err = stBrowse, msg.err
		return s, nil
	}
	s.info, s.err = msg.info, nil
	if msg.poll {
		return s.afterConfirm()
	}
	if s.info.Project {
		return s, s.create("")
	}
	if s.info.Big() {
		s.stage = stConfirm
		return s, nil
	}
	return s.afterConfirm()
}

// afterConfirm: folder zaakceptowany - zostaje wybrac set albo poczekac na niego.
func (s Start) afterConfirm() (tea.Model, tea.Cmd) {
	var ready []string
	for _, set := range s.info.Sets {
		if setReady(s.info.Dir, set) {
			ready = append(ready, set)
		}
	}
	switch {
	case len(ready) == 1:
		s.info.Sets = ready
		return s, s.create(ready[0])
	case len(ready) > 1:
		s.info.Sets, s.stage, s.cur = ready, stPickSet, 0
		return s, nil
	}
	s.stage = stWaitSet
	dir := s.info.Dir
	return s, tea.Tick(2*s.pollWait, func(time.Time) tea.Msg { return inspect(dir, true)() })
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
		case stMenu:
			return s, tea.Quit
		case stName:
			return s.toMenu(), nil
		case stMkdir, stConfirm, stPickSet, stWaitSet:
			s.stage, s.err = stBrowse, nil
			return s, nil
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
		return s.keyMenu(k)
	case stBrowse:
		return s.keyBrowse(k)
	case stConfirm:
		switch k.String() {
		case "t", "y", "enter":
			return s.afterConfirm()
		case "n":
			s.stage = stBrowse
		}
		return s, nil
	case stPickSet:
		switch k.String() {
		case "up", "k":
			if s.cur > 0 {
				s.cur--
			}
		case "down", "j":
			if s.cur < len(s.info.Sets)-1 {
				s.cur++
			}
		case "enter":
			return s, s.create(s.info.Sets[s.cur])
		}
		return s, nil
	case stWait, stWaitSet:
		return s, nil
	}
	// pola tekstowe: nazwa, nowy folder, kod, folder na projekt
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

func (s Start) keyMenu(k tea.KeyMsg) (tea.Model, tea.Cmd) {
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
}

func (s Start) keyBrowse(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "up", "k":
		if s.br.cur > 0 {
			s.br.cur--
		}
	case "down", "j":
		if s.br.cur < len(s.br.dirs)-1 {
			s.br.cur++
		}
	case "enter", "right", "l":
		s.br.enter()
		s.err = nil
	case "left", "h", "backspace":
		s.br.up()
		s.err = nil
	case "s", " ":
		if s.br.path == "" {
			return s, nil
		}
		s.err = nil
		return s, inspect(s.br.path, false)
	case "+":
		if s.br.path != "" {
			s.stage, s.input, s.err = stMkdir, "", nil
		}
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
		s.stage, s.br = stBrowse, newBrowser(s.browseFrom)
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
	case stMkdir:
		name := strings.TrimSpace(s.input)
		if name == "" || strings.ContainsAny(name, `<>:"/\|?*`) || name == "." || name == ".." {
			s.err = errors.New("niepoprawna nazwa folderu")
			return s, nil
		}
		dir := filepath.Join(s.br.path, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			s.err = err
			return s, nil
		}
		s.br.path = dir
		s.br.load()
		s.stage, s.err = stBrowse, nil
	case stCode:
		inv, err := syncthing.ParseInvite(s.input)
		if err != nil {
			s.err = err
			return s, nil
		}
		s.inv, s.err, s.stage = inv, nil, stDir
		s.input = filepath.Join(s.baseDir, inv.Name)
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
		b.WriteString(cursor(s.cur == n+miNew) + sKey.Render("n") + "  Nowy projekt" + sDim.Render(" - wybierz folder sesji i zapros innych") + "\n")
		b.WriteString(cursor(s.cur == n+miJoin) + sKey.Render("d") + "  Dolacz do projektu" + sDim.Render(" - masz kod zaproszenia") + "\n")
		b.WriteString(cursor(s.cur == n+miQuit) + sKey.Render("q") + "  Wyjscie\n")
		b.WriteString("\n" + keys("↑↓", "wybor", "enter", "otworz"))
	case stBrowse, stMkdir:
		b.WriteString(s.viewBrowser(w))
	case stConfirm:
		b.WriteString(sBold.Render("Duzy folder") + "\n\n")
		b.WriteString(s.info.Dir + "\n")
		b.WriteString(lipgloss.NewStyle().Foreground(cWarn).Render("  "+humanCount(s.info)) + "\n\n")
		b.WriteString("Wszystko z tego folderu poleci do kazdego uczestnika sesji. Na pewno?\n\n")
		b.WriteString(keys("t", "tak, udostepnij", "n", "wybierz inny"))
	case stPickSet:
		b.WriteString(sBold.Render("Ktory set jest projektem tej sesji?") + "\n")
		b.WriteString(sDim.Render("Pozostale sety zostana w folderze, ale nie beda scalane.") + "\n\n")
		for i, set := range s.info.Sets {
			b.WriteString(cursor(i == s.cur) + set + "\n")
		}
		b.WriteString("\n" + keys("↑↓", "wybor", "enter", "udostepnij", "esc", "wstecz"))
	case stWaitSet:
		b.WriteString(sBold.Render("Zapisz projekt z Live do tego folderu") + "\n\n")
		b.WriteString("  " + lipgloss.NewStyle().Bold(true).Foreground(cAccent).Render(s.info.Dir) + "\n\n")
		b.WriteString("W Live: " + sBold.Render("File > Save Live Set As...") + " i wybierz ten folder.\n")
		b.WriteString(sDim.Render("Live moze zalozyc w nim podfolder \"<nazwa> Project\" - to w porzadku.") + "\n\n")
		b.WriteString(lipgloss.NewStyle().Foreground(cWarn).Render("⟳ czekam na plik .als...") + "\n\n")
		b.WriteString(keys("esc", "wstecz"))
	case stCode:
		b.WriteString(sBold.Render("Dolacz do projektu") + "\n")
		b.WriteString(sDim.Render("Wklej kod zaproszenia od osoby, ktora zaprasza (zaczyna sie od ABC1).") + "\n\n")
		b.WriteString(inputBox(s.input, w) + "\n")
		b.WriteString(keys("enter", "dalej", "ctrl+u", "wyczysc", "esc", "menu"))
	case stDir:
		b.WriteString(sBold.Render("Projekt "+s.inv.Name) + sDim.Render(" od "+s.inv.Host) + "\n")
		b.WriteString(sDim.Render("Gdzie zapisac folder sesji? Musi byc nowy albo pusty.") + "\n\n")
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

func (s Start) viewBrowser(w int) string {
	var b strings.Builder
	br := s.br
	b.WriteString(sBold.Render("Nowy projekt - wybierz folder sesji") + "\n")
	b.WriteString(sDim.Render("Caly ten folder (set, sample, nagrania, .mp3...) bedzie wspolny dla wszystkich w sesji.") + "\n\n")
	where := br.path
	if where == "" {
		where = "Dyski"
	}
	b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(cAccent).Render(trunc(where, w-4)) + "\n")
	if br.err != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(cBad).Render("  "+br.err.Error()) + "\n")
	}
	listH := max(5, s.height-16)
	start := max(0, min(br.cur-listH/2, len(br.dirs)-listH))
	if len(br.dirs) == 0 && br.err == nil {
		b.WriteString(sDim.Render("  (brak podfolderow)") + "\n")
	}
	for i := start; i < min(len(br.dirs), start+listH); i++ {
		name := br.dirs[i]
		if br.path != "" {
			name += string(os.PathSeparator)
		}
		b.WriteString(cursor(i == br.cur) + trunc(name, w-6) + "\n")
	}
	if br.path != "" && br.files > 0 {
		b.WriteString(sDim.Render(fmt.Sprintf("  + %d plik(ow) w tym folderze", br.files)) + "\n")
	}
	if s.stage == stMkdir {
		b.WriteString("\n" + sBold.Render("Nazwa nowego folderu:") + "\n")
		b.WriteString(inputBox(s.input, w) + "\n")
		b.WriteString(keys("enter", "zaloz", "esc", "anuluj"))
		return b.String()
	}
	b.WriteString("\n" + keys("↑↓", "wybor", "enter/→", "wejdz", "←", "wyzej", "s", "wybierz ten folder", "+", "nowy folder", "esc", "menu"))
	return b.String()
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

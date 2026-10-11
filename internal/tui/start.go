package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/c-airr/ableton-collab/store"
	"github.com/c-airr/ableton-collab/transport/syncthing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Joiner: transport od strony "nie mam jeszcze projektu" (*syncthing.Node
// przez adapter w main - zwraca projekt jako Network, zeby dalo sie testowac).
type Joiner interface {
	DeviceID() string
	Folders() ([]syncthing.FolderConfig, error)
	Join(inv syncthing.Invite, dir string) (Network, error)
	Share(dir, folderID, label string) (Network, error)
}

type startStage int

const (
	stPick startStage = iota // wybor znanego projektu
	stCode                   // wklejanie kodu
	stDir                    // gdzie zalozyc folder
	stWait                   // czekanie na akceptacje i pobranie
)

// Start: ekran poza projektem - wybor projektu albo dolaczenie kodem.
// Po zakonczeniu Dir() to folder projektu do otwarcia ("" - wyjscie).
type Start struct {
	net     Joiner
	me      store.Device
	baseDir string // domyslne miejsce na nowe projekty

	stage    startStage
	projects []syncthing.FolderConfig
	cur      int
	input    string
	inv      syncthing.Invite
	err      error
	waitMsg  string
	share    Network
	dir      string
	done     bool
	width    int
	pollWait time.Duration
}

func NewStart(net Joiner, me store.Device, baseDir, code string) Start {
	s := Start{net: net, me: me, baseDir: baseDir, width: 100, pollWait: time.Second}
	if code != "" {
		s.stage, s.input = stCode, code
	}
	return s
}

// Dir: wybrany albo pobrany folder projektu ("" - uzytkownik wyszedl).
func (s Start) Dir() string {
	if s.done {
		return s.dir
	}
	return ""
}

type projectsMsg struct {
	list []syncthing.FolderConfig
	err  error
}

type joinedMsg struct {
	share Network
	err   error
}

type waitMsg struct {
	text  string
	ready bool
	err   error
}

func (s Start) Init() tea.Cmd {
	if s.stage == stCode && s.input != "" {
		return func() tea.Msg { return tea.KeyMsg{Type: tea.KeyEnter} }
	}
	net := s.net
	return func() tea.Msg {
		fs, err := net.Folders()
		var out []syncthing.FolderConfig
		for _, f := range fs {
			if strings.HasPrefix(f.ID, "abcollab-") {
				out = append(out, f)
			}
		}
		return projectsMsg{out, err}
	}
}

func (s Start) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width = msg.Width
	case projectsMsg:
		s.projects, s.err = msg.list, msg.err
		if len(s.projects) == 0 && s.stage == stPick {
			s.stage = stCode
		}
	case joinedMsg:
		if msg.err != nil {
			s.stage, s.err = stDir, msg.err
			return s, nil
		}
		s.share, s.stage = msg.share, stWait
		return s, s.poll()
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

func (s Start) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "ctrl+c":
		return s, tea.Quit
	case "esc":
		switch s.stage {
		case stCode:
			if len(s.projects) == 0 {
				return s, tea.Quit
			}
			s.stage, s.err = stPick, nil
		case stDir:
			s.stage, s.input, s.err = stCode, s.inv.Code(), nil
		case stPick, stWait:
			return s, tea.Quit
		}
		return s, nil
	}
	switch s.stage {
	case stPick:
		switch k.String() {
		case "q":
			return s, tea.Quit
		case "up", "k":
			if s.cur > 0 {
				s.cur--
			}
		case "down", "j":
			if s.cur < len(s.projects) {
				s.cur++
			}
		case "d":
			s.stage, s.input, s.err = stCode, "", nil
		case "enter":
			if s.cur == len(s.projects) {
				s.stage, s.input, s.err = stCode, "", nil
				return s, nil
			}
			f := s.projects[s.cur]
			if _, err := store.Open(f.Path, s.me); errors.Is(err, store.ErrNotProject) {
				// dolaczanie przerwane wczesniej - projekt jeszcze nie doszedl
				sh, err := s.net.Share(f.Path, f.ID, f.Label)
				if err != nil {
					s.err = err
					return s, nil
				}
				s.share, s.dir, s.stage = sh, f.Path, stWait
				s.inv = syncthing.Invite{Name: f.Label, Host: "zapraszajacy"}
				return s, s.poll()
			}
			s.dir, s.done = f.Path, true
			return s, tea.Quit
		}
	case stCode, stDir:
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
	}
	return s, nil
}

func (s Start) submit() (tea.Model, tea.Cmd) {
	switch s.stage {
	case stCode:
		inv, err := syncthing.ParseInvite(s.input)
		if err != nil {
			s.err = err
			return s, nil
		}
		s.inv, s.err, s.stage = inv, nil, stDir
		s.input = filepath.Join(s.baseDir, inv.Name+" Project")
	case stDir:
		dir := strings.TrimSpace(s.input)
		if dir == "" {
			return s, nil
		}
		s.dir, s.err = dir, nil
		net, inv := s.net, s.inv
		return s, func() tea.Msg {
			sh, err := net.Join(inv, dir)
			return joinedMsg{sh, err}
		}
	}
	return s, nil
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

func (s Start) View() string {
	w := max(40, s.width)
	var b strings.Builder
	b.WriteString(sTitle.Render("ableton-collab") + sDim.Render("  ·  "+s.me.Name) + "\n\n")
	switch s.stage {
	case stPick:
		b.WriteString(sBold.Render("Twoje projekty") + "\n")
		for i, p := range s.projects {
			line := fmt.Sprintf("%-24s %s", trunc(p.Label, 24), sDim.Render(trunc(p.Path, w-32)))
			b.WriteString(cursor(i == s.cur) + line + "\n")
		}
		b.WriteString(cursor(s.cur == len(s.projects)) + "dolacz do projektu kodem zaproszenia\n")
		b.WriteString("\n" + keys("enter", "otworz", "d", "dolacz kodem", "q", "wyjscie"))
	case stCode:
		b.WriteString(sBold.Render("Dolacz do projektu") + "\n")
		b.WriteString(sDim.Render("Wklej kod zaproszenia od osoby, ktora zaprasza (zaczyna sie od ABC1).") + "\n")
		b.WriteString(sDim.Render("Nowy projekt w folderze z setem zakladasz komenda: abcollab init <set.als>") + "\n\n")
		b.WriteString(inputBox(s.input, w) + "\n")
		b.WriteString(keys("enter", "dalej", "ctrl+u", "wyczysc", "esc", "wstecz"))
	case stDir:
		b.WriteString(sBold.Render("Projekt "+s.inv.Name) + sDim.Render(" od "+s.inv.Host) + "\n")
		b.WriteString(sDim.Render("Gdzie zapisac projekt? Folder musi byc nowy albo pusty.") + "\n\n")
		b.WriteString(inputBox(s.input, w) + "\n")
		b.WriteString(keys("enter", "dolacz", "esc", "wstecz"))
	case stWait:
		b.WriteString(sBold.Render("Dolaczanie do "+s.inv.Name) + "\n\n")
		b.WriteString("Twoj kod urzadzenia: " + lipgloss.NewStyle().Bold(true).Foreground(cAccent).Render(syncthing.ShortID(s.net.DeviceID())) + "\n")
		b.WriteString(sDim.Render(s.inv.Host+" zobaczy go przy twojej prosbie - podaj mu go, zeby wiedzial, ze to ty.") + "\n\n")
		msg := s.waitMsg
		if msg == "" {
			msg = "Laczenie..."
		}
		b.WriteString(lipgloss.NewStyle().Foreground(cWarn).Render("⟳ "+msg) + "\n")
		b.WriteString(sDim.Render("folder: "+s.dir) + "\n\n")
		b.WriteString(keys("esc", "przerwij (prosba zostaje - mozesz wrocic pozniej)"))
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

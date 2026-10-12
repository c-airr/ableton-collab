// Package tui to interfejs terminalowy abcollab (Bubble Tea): jeden panel
// (pliki sesji | logo + log | ludzie, pod spodem przyciski i pasek
// komunikatow) plus okna dialogowe (Join, Host, ustawienia, konflikty).
//
// Operacje na projekcie (store) ida jako tea.Cmd poza petla UI - jedna naraz
// (session.busy), bo store nie jest przygotowany na rownolegle zapisy.
package tui

import (
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/c-airr/ableton-collab/internal/live"
	"github.com/c-airr/ableton-collab/store"
	"github.com/c-airr/ableton-collab/transport/syncthing"

	tea "github.com/charmbracelet/bubbletea"
)

// Backend: reszta programu (w main: Syncthing + store + konfiguracja).
type Backend interface {
	DeviceID() string
	Rename(name string) error
	Sessions() ([]SessionRef, error)
	// Open otwiera istniejaca sesje (udostepnia folder, jesli trzeba).
	Open(dir string) (*store.Project, Network, error)
	// Host zaklada sesje w folderze z setem (set wzgledem dir, "/").
	Host(dir, set string) (*store.Project, Network, error)
	// Join prosi zapraszajacego o dolaczenie; existing - folder z wlasnymi plikami.
	Join(inv syncthing.Invite, dir string, existing bool) (Network, error)
	// SetLast zapamietuje sesje otwierana przy starcie ("" - zadna).
	SetLast(dir string)
	// Live: synchronizacja na zywo z otwartym Live'em (nil - niedostepna).
	Live(p *store.Project) LiveSync
	Settings() Settings
	SetSettings(Settings) error
	// SaveInLive wysyla Ctrl+S do Live'a, jesli jest na wierzchu.
	SaveInLive() (bool, error)
	// OpenInLive otwiera set w Live.
	OpenInLive(path string) error
}

// Settings: przelaczniki w Settings (pamietane miedzy uruchomieniami).
type Settings struct {
	AutoSync bool `json:"autoSync"` // zapis z Live'a -> wersja, zmiany innych pobierane same
	AutoSave bool `json:"autoSave"` // abcollab sam wciska Ctrl+S w Live po zmianach
}

// LiveSync: silnik pracy na zywo jednej sesji (*live.Engine).
type LiveSync interface {
	Notes() <-chan live.Note
	Status() live.Status
	Resolve(key string, mine bool)
	Recheck()
	Close()
}

type SessionRef struct {
	Name, Dir string
}

// Network: transport jednej sesji (*syncthing.Project). nil - bez sieci.
type Network interface {
	Lobby() (*syncthing.Lobby, error)
	Invite(myName string) string
	Accept(syncthing.Request) error
	Reject(syncthing.Request) error
	Kick(syncthing.Member) error
	Events(since int, timeout time.Duration) ([]syncthing.Event, int, error)
}

type Options struct {
	Me      store.Device
	AskName bool   // pierwsze uruchomienie - pytamy o nick
	Last    string // sesja do otwarcia przy starcie
	// BaseDir: domyslne miejsce na nowe foldery przy dolaczaniu,
	// BrowseFrom: start pola folderu przy hostowaniu.
	BaseDir, BrowseFrom string
	Offline             bool   // bez sieci (tylko -offline z CLI)
	JoinCode            string // `abcollab join <kod>` - od razu dolaczanie
}

type focus int

const (
	fFiles focus = iota
	fCenter
	fPeople
	fButtons
)

type App struct {
	be   Backend
	opts Options
	me   store.Device

	w, h   int
	focus  focus
	btn    int
	ses    *session
	sid    int // numer sesji - odrzucamy spoznione komunikaty po zmianie
	center centerMode
	dlg    dialog
	note   note // chwilowy komunikat na pasku

	pollEvery time.Duration
	now       func() time.Time
	settings  Settings
}

type centerMode int

const (
	cLog centerMode = iota
	cHistory
)

type note struct {
	text  string
	level level
	until time.Time
}

type level int

const (
	lInfo level = iota
	lOK
	lWarn
	lErr
	lFile
)

func New(be Backend, o Options) App {
	a := App{be: be, opts: o, me: o.Me, w: 100, h: 30, focus: fButtons, pollEvery: 2 * time.Second, now: time.Now,
		settings: be.Settings()}
	if o.Last != "" {
		a.sid = 1
	}
	var next dialog
	if o.JoinCode != "" && !o.Offline {
		next = newJoinDialogWithCode(o.JoinCode)
	}
	a.dlg = next
	if o.AskName {
		nd := newNameDialog(o.Me.Name, true)
		nd.next = next
		a.dlg = nd
	}
	return a
}

// --- komunikaty ------------------------------------------------------------------------

type tickMsg struct{}

type openedMsg struct {
	sid     int
	p       *store.Project
	net     Network
	err     error
	invite  bool // nowa sesja - pokaz zaproszenie
	verbose string
}

func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

func (a App) Init() tea.Cmd {
	cmds := []tea.Cmd{tick(a.pollEvery)}
	if a.opts.Last != "" {
		// sid ustawiony w New - Init dziala na kopii modelu
		sid, be, dir := a.sid, a.be, a.opts.Last
		cmds = append(cmds, func() tea.Msg {
			p, net, err := be.Open(dir)
			return openedMsg{sid: sid, p: p, net: net, err: err}
		})
	}
	return tea.Batch(cmds...)
}

func (a *App) openCmd(dir string, invite bool) tea.Cmd {
	a.sid++
	sid, be := a.sid, a.be
	return func() tea.Msg {
		p, net, err := be.Open(dir)
		return openedMsg{sid: sid, p: p, net: net, err: err, invite: invite}
	}
}

func (a *App) say(lv level, text string) {
	a.note = note{text: text, level: lv, until: a.now().Add(6 * time.Second)}
}

// --- Update ----------------------------------------------------------------------------

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.w, a.h = msg.Width, msg.Height
		return a, nil

	case tickMsg:
		cmds := []tea.Cmd{tick(a.pollEvery)}
		if a.ses != nil && a.ses.busy == "" {
			a.ses.busy = refreshing
			cmds = append(cmds, a.ses.refreshCmd(a.sid))
		}
		if a.dlg != nil {
			var cmd tea.Cmd
			a.dlg, cmd = a.dlg.update(&a, msg)
			cmds = append(cmds, cmd)
		}
		return a, tea.Batch(cmds...)

	case openedMsg:
		if msg.sid != a.sid {
			return a, nil
		}
		if msg.err != nil {
			a.say(lErr, msg.err.Error())
			a.be.SetLast("")
			return a, nil
		}
		cmd := a.startSession(msg.p, msg.net)
		if msg.invite && msg.net != nil {
			a.dlg = newInviteDialog(&a)
		}
		return a, cmd

	case sessionMsg:
		if a.ses == nil || msg.sessionID() != a.sid {
			return a, nil
		}
		return a.ses.handle(&a, msg)

	case tea.MouseMsg:
		return a.mouse(msg)

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}
		if a.dlg != nil {
			d, cmd := a.dlg.update(&a, msg)
			a.dlg = d
			return a, cmd
		}
		return a.key(msg)
	}
	if a.dlg != nil {
		d, cmd := a.dlg.update(&a, msg)
		a.dlg = d
		return a, cmd
	}
	return a, nil
}

// startSession: sesja otwarta - od teraz panel ja pokazuje.
func (a *App) startSession(p *store.Project, net Network) tea.Cmd {
	if a.ses != nil && a.ses.live != nil {
		a.ses.live.Close()
	}
	a.ses = newSession(p, net, a.now)
	a.ses.tree.expandTo(p.Meta.Set)
	a.ses.tree.sel = p.Meta.Set
	a.ses.logf(lInfo, "Opened session %s", sessionName(p))
	a.be.SetLast(p.Dir)
	a.focus, a.btn, a.center = fFiles, 0, cLog
	cmds := []tea.Cmd{a.ses.refreshCmd(a.sid)}
	a.ses.busy = refreshing
	if net != nil {
		cmds = append(cmds, a.ses.eventsCmd(a.sid, 0))
	}
	if ls := a.be.Live(p); ls != nil {
		a.ses.live = ls
		cmds = append(cmds, a.ses.liveCmd(a.sid))
	}
	return tea.Batch(cmds...)
}

// --- mysz ---------------------------------------------------------------------------------

func (a App) mouse(m tea.MouseMsg) (tea.Model, tea.Cmd) {
	l := a.layout()
	wheel := 0
	switch m.Button {
	case tea.MouseButtonWheelUp:
		wheel = -1
	case tea.MouseButtonWheelDown:
		wheel = 1
	case tea.MouseButtonLeft:
		if m.Action != tea.MouseActionPress {
			return a, nil
		}
	default:
		return a, nil
	}
	if a.dlg != nil {
		if wheel != 0 {
			return a, nil
		}
		f := a.dlg.form(&a)
		b, opts, sugg := f.render(l.w)
		x0, y0 := dialogPos(l, b)
		row := m.Y - y0
		if m.X < x0 || m.X >= x0+lipgloss.Width(b) {
			return a, nil
		}
		if i, ok := sugg[row]; ok && f.path != nil {
			f.path.complete(i)
			return a, nil
		}
		if i, ok := opts[row]; ok {
			var cmd tea.Cmd
			a.dlg, cmd = a.dlg.update(&a, chooseMsg{i})
			return a, cmd
		}
		return a, nil
	}
	// przyciski
	if wheel == 0 {
		for i, r := range l.btns {
			if r.has(m.X, m.Y) {
				a.focus, a.btn = fButtons, i
				return a.press(a.buttons()[i].id)
			}
		}
	}
	right := m.X >= l.leftW+l.centerW
	if m.Y >= l.topH { // dol: ACTIONS | LOG
		if right && a.ses != nil {
			a.focus = fCenter
			if wheel != 0 {
				a.ses.centerKey(a.center, map[int]string{-1: "up", 1: "down"}[wheel])
			}
		} else if !right {
			a.focus = fButtons
		}
		return a, nil
	}
	if a.ses == nil {
		return a, nil
	}
	s := a.ses
	switch {
	case m.X < l.leftW: // pliki
		a.focus = fFiles
		if wheel != 0 {
			s.tree.key(map[int]string{-1: "up", 1: "down"}[wheel])
		} else if m.Y >= 1 && m.Y < l.topH-1 {
			s.tree.click(m.Y - 1)
		}
	case right: // ludzie
		a.focus = fPeople
		if wheel != 0 {
			s.peopleKey(map[int]string{-1: "up", 1: "down"}[wheel])
		} else if m.Y >= 1 {
			_, idx := a.peopleRows(l.rightW - 4)
			if r := m.Y - 1; r < len(idx) && idx[r] >= 0 {
				s.peerCur = idx[r]
			}
		}
	}
	return a, nil
}

func (a App) leave() (tea.Model, tea.Cmd) {
	if a.ses != nil {
		a.say(lInfo, "Left session "+sessionName(a.ses.p)+" - reopen it from Settings")
	}
	a.sid++
	if a.ses != nil && a.ses.live != nil {
		a.ses.live.Close()
	}
	a.ses = nil
	a.center = cLog
	a.focus, a.btn = fButtons, 0
	a.be.SetLast("")
	return a, nil
}

func (a App) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.note.text != "" && k.String() == "esc" {
		a.note = note{}
		return a, nil
	}
	switch k.String() {
	case "tab":
		a.focus = a.nextFocus(1)
		return a, nil
	case "shift+tab":
		a.focus = a.nextFocus(-1)
		return a, nil
	case "esc":
		a.note = note{}
		return a, nil
	}
	// strzalki i enter w aktywnym panelu
	switch a.focus {
	case fFiles:
		if a.ses != nil && a.ses.tree.key(k.String()) {
			return a, nil
		}
		if k.String() == "delete" {
			return a.press("delete")
		}
	case fPeople:
		if a.ses != nil && a.ses.peopleKey(k.String()) {
			return a, nil
		}
	case fCenter:
		if a.ses != nil && a.ses.centerKey(a.center, k.String()) {
			return a, nil
		}
	case fButtons:
		bs := a.buttons()
		switch k.String() {
		case "left", "up":
			if a.btn > 0 {
				a.btn--
			}
			return a, nil
		case "right", "down":
			if a.btn < len(bs)-1 {
				a.btn++
			}
			return a, nil
		case "enter", " ":
			if a.btn < len(bs) {
				return a.press(bs[a.btn].id)
			}
		}
	}
	for _, b := range a.buttons() {
		if b.key == k.String() {
			return a.press(b.id)
		}
	}
	return a, nil
}

func (a App) nextFocus(dir int) focus {
	order := []focus{fFiles, fPeople, fButtons, fCenter}
	if a.ses == nil {
		return fButtons
	}
	i := 0
	for j, f := range order {
		if f == a.focus {
			i = j
		}
	}
	return order[(i+dir+len(order))%len(order)]
}

// --- przyciski -------------------------------------------------------------------------

type button struct {
	id, label, key string
	danger         bool
}

func (a App) buttons() []button {
	if a.ses == nil {
		return []button{
			{id: "join", label: "Join", key: "j"},
			{id: "host", label: "Host", key: "h"},
			{id: "settings", label: "Settings", key: "o"},
			{id: "quit", label: "Quit", key: "q"},
		}
	}
	s := a.ses
	bs := []button{
		{id: "save", label: "Save", key: "s"},
		{id: "get", label: "Get changes", key: "g"},
	}
	if s.net != nil {
		bs = append(bs, button{id: "invite", label: "Invite", key: "i"})
	}
	hist := "History"
	if a.center == cHistory {
		hist = "Log"
	}
	bs = append(bs, button{id: "history", label: hist, key: "y"})
	if len(s.reload) > 0 {
		bs = append(bs, button{id: "reload", label: "Reload set", key: "u"})
	}
	if !s.liveOn() {
		bs = append(bs, button{id: "openlive", label: "Open in Live", key: "e"})
	}
	// przyciski zalezne od zaznaczenia
	switch {
	case a.focus == fFiles && s.tree.selectedDeletable(s.p) != "":
		bs = append(bs, button{id: "delete", label: "Delete", key: "x", danger: true})
	case a.focus == fPeople && s.selRequest() != nil:
		bs = append(bs, button{id: "accept", label: "Accept", key: "a"}, button{id: "reject", label: "Reject", key: "r", danger: true})
	case a.focus == fPeople && s.selMember() != nil && s.isHost():
		bs = append(bs, button{id: "kick", label: "Kick", key: "k", danger: true})
	}
	bs = append(bs,
		button{id: "settings", label: "Settings", key: "o"},
		button{id: "leave", label: "Leave", key: "l"},
		button{id: "quit", label: "Quit", key: "q"})
	return bs
}

func (a App) press(id string) (tea.Model, tea.Cmd) {
	s := a.ses
	switch id {
	case "quit":
		return a, tea.Quit
	case "settings":
		a.dlg = newSettingsDialog(&a)
		return a, nil
	case "join":
		if a.opts.Offline {
			a.say(lWarn, "Offline mode - joining needs the network")
			return a, nil
		}
		a.dlg = newJoinDialog(&a)
		return a, nil
	case "host":
		if a.opts.Offline {
			a.say(lWarn, "Offline mode - hosting needs the network")
			return a, nil
		}
		a.dlg = newHostDialog(&a)
		return a, nil
	case "leave":
		return a.leave()
	case "history":
		if a.center == cHistory {
			a.center = cLog
		} else {
			a.center = cHistory
		}
		return a, nil
	}
	if s == nil {
		return a, nil
	}
	switch id {
	case "invite":
		a.dlg = newInviteDialog(&a)
		return a, nil
	case "openlive":
		path := s.p.WorkingPath()
		if err := a.be.OpenInLive(path); err != nil {
			a.say(lErr, err.Error())
			return a, nil
		}
		s.logf(lInfo, "Opening %s in Live: %s", s.p.SetName(), path)
		return a, nil
	case "reload":
		a.dlg = newConfirmDialog("Reload "+s.p.SetName()+"?",
			[]string{"The set file gets the latest version with everyone's changes. Save in Live first if you changed something, then reload it in Live: File > Open Recent > " + s.p.SetName() + " (don't save the old one)."},
			"Reload", false, func(a *App) tea.Cmd { return a.ses.reloadCmd(a.sid) })
		return a, nil
	case "delete":
		path := s.tree.selectedDeletable(s.p)
		if path == "" {
			return a, nil
		}
		a.dlg = newConfirmDialog("Delete "+relName(s.p.Dir, path)+"?",
			[]string{"It will be deleted for everyone in the session."}, "Delete", true,
			func(a *App) tea.Cmd { return a.ses.deleteCmd(a.sid, path) })
		return a, nil
	case "kick":
		m := s.selMember()
		if m == nil {
			return a, nil
		}
		mem := *m
		a.dlg = newConfirmDialog("Kick "+mem.Name+"?",
			[]string{"They stop getting changes from you and can't rejoin with the old code."}, "Kick", true,
			func(a *App) tea.Cmd { return a.ses.lobbyCmd(a.sid, "kick", nil, &mem) })
		return a, nil
	}
	if s.busy != "" && s.busy != refreshing {
		a.say(lWarn, "Wait - "+s.busy)
		return a, nil
	}
	switch id {
	case "save":
		s.busy = "saving"
		return a, s.commitCmd(a.sid)
	case "get":
		s.busy = "getting changes"
		return a, s.pullCmd(a.sid, nil, s.liveOn())
	case "accept":
		r := s.selRequest()
		if r == nil {
			return a, nil
		}
		if s.lobby != nil && s.lobby.Full() {
			a.say(lWarn, "The session is full")
			return a, nil
		}
		req := *r
		return a, s.lobbyCmd(a.sid, "accept", &req, nil)
	case "reject":
		r := s.selRequest()
		if r == nil {
			return a, nil
		}
		req := *r
		return a, s.lobbyCmd(a.sid, "reject", &req, nil)
	}
	return a, nil
}

// Package tui to interfejs terminalowy abcollab (Bubble Tea).
//
// Wszystkie operacje na projekcie (status, commit, pull) ida przez store i sa
// wykonywane jako tea.Cmd poza petla UI - jedna naraz (pole busy), bo store
// nie jest przygotowany na rownolegle zapisy. Operacje lobby (przyjmij,
// odrzuc) ida przez transport i nie dotykaja store.
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/c-airr/ableton-collab/als"
	"github.com/c-airr/ableton-collab/store"
	"github.com/c-airr/ableton-collab/transport/syncthing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type tab int

const (
	tabStatus tab = iota
	tabLobby
	tabHistory
	tabProject
)

var tabNames = []string{"Status", "Lobby", "Historia", "Projekt"}

// Network: transport projektu (*syncthing.Project). nil - praca bez sieci.
type Network interface {
	Lobby() (*syncthing.Lobby, error)
	Invite(myName string) string
	Accept(syncthing.Request) error
	Reject(syncthing.Request) error
}

const refreshEvery = 3 * time.Second

type Model struct {
	p   *store.Project
	net Network

	tab     tab
	st      *store.Status
	log     []*store.Version
	assets  []als.Warning
	err     error
	busy    string   // trwajaca operacja ("" - bezczynny)
	msg     []string // wynik ostatniej operacji
	msgWarn bool
	histCur int

	conflict *conflictState

	lobby    *syncthing.Lobby
	lobbyErr error
	reqCur   int
	invite   string // pokazany kod zaproszenia
	copied   bool   // kod trafil do schowka

	width, height int
}

type conflictState struct {
	with   store.Device
	list   []als.Conflict
	cur    int
	choice map[string]als.Resolution
}

func New(p *store.Project, net Network) Model {
	return Model{p: p, net: net, width: 100, height: 30, busy: "wczytywanie"}
}

// ShowInvite: start od razu z kodem zaproszenia (projekt wlasnie zalozony).
func (m Model) ShowInvite() Model {
	if m.net != nil {
		m.tab = tabLobby
		m.invite = m.net.Invite(m.p.Me.Name)
		m.copied = clipboard(m.invite) == nil
	}
	return m
}

// --- komunikaty ----------------------------------------------------------------------

type refreshMsg struct {
	st       *store.Status
	log      []*store.Version
	assets   []als.Warning
	lobby    *syncthing.Lobby
	lobbyErr error
	err      error
}

type tickMsg struct{}

type actionMsg struct {
	lines []string
	warn  bool
	pull  *store.PullResult
	err   error
}

func (m Model) refreshCmd(withAssets bool) tea.Cmd {
	p, net := m.p, m.net
	return func() tea.Msg {
		var lobby *syncthing.Lobby
		var lobbyErr error
		if net != nil {
			lobby, lobbyErr = net.Lobby()
		}
		st, err := p.Status()
		if err != nil {
			return refreshMsg{err: err, lobby: lobby, lobbyErr: lobbyErr}
		}
		vs, err := p.Versions()
		if err != nil {
			return refreshMsg{err: err}
		}
		r := refreshMsg{st: st, log: store.Log(vs), lobby: lobby, lobbyErr: lobbyErr}
		if withAssets {
			if doc, err := als.ReadFile(p.WorkingPath()); err == nil {
				r.assets = als.AssetWarnings(doc, p.Dir)
			}
		}
		return r
	}
}

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) commitCmd() tea.Cmd {
	p := m.p
	return func() tea.Msg {
		if p.Head() == "" {
			return actionMsg{lines: []string{"Nie masz jeszcze wersji projektu - najpierw pobierz (p)."}, warn: true}
		}
		v, err := p.Commit()
		if err != nil {
			return actionMsg{err: err}
		}
		if v == nil {
			return actionMsg{lines: []string{"Brak zmian do zapisania."}}
		}
		return actionMsg{lines: append([]string{"Zapisano wersje " + v.Short() + ":"}, v.Summary...)}
	}
}

func (m Model) pullCmd(res map[string]als.Resolution) tea.Cmd {
	p := m.p
	return func() tea.Msg {
		r, err := p.Pull(res)
		if err != nil {
			return actionMsg{err: err}
		}
		lines, warn := pullReport(p, r)
		return actionMsg{lines: lines, warn: warn, pull: r}
	}
}

func pullReport(p *store.Project, r *store.PullResult) ([]string, bool) {
	var out []string
	warn := false
	if r.Committed != nil {
		out = append(out, "Zapisano twoje zmiany jako wersje "+r.Committed.Short()+".")
	}
	if r.Backup != "" {
		out = append(out, "Twoj dotychczasowy set skopiowano do: "+r.Backup)
	}
	for _, d := range r.FastForwarded {
		out = append(out, "Przyjeto zmiany od "+d.Name+".")
	}
	for _, v := range r.Merged {
		out = append(out, "Scalono (wersja "+v.Short()+"):")
		out = append(out, v.Summary...)
	}
	for _, w := range r.Warnings {
		out = append(out, w.String())
		warn = true
	}
	for _, d := range r.Incomplete {
		out = append(out, "Zmiany od "+d.Name+" jeszcze sie synchronizuja.")
	}
	if len(r.Conflicts) > 0 {
		out = append(out, fmt.Sprintf("%d konflikt(y) z %s - wybierz, co zachowac.", len(r.Conflicts), r.ConflictWith.Name))
		warn = true
	}
	if r.WorkingChanged() {
		out = append(out, "", p.Meta.Set+" zostal zaktualizowany. Jesli masz go otwartego w Live, otworz go",
			"ponownie (File > Open Recent) i NIE zapisuj starej wersji - nadpisalbys zmiany innych.")
		warn = true
	}
	if len(out) == 0 {
		out = []string{"Wszystko aktualne."}
	}
	return out, warn
}

// --- Update ----------------------------------------------------------------------------

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refreshCmd(true), tick())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		if m.busy == "" && m.conflict == nil {
			m.busy = "odswiezanie"
			return m, tea.Batch(m.refreshCmd(false), tick())
		}
		return m, tick()

	case refreshMsg:
		m.busy = ""
		m.err = msg.err
		m.lobby, m.lobbyErr = msg.lobby, msg.lobbyErr
		if m.lobby != nil && m.reqCur >= len(m.lobby.Requests) {
			m.reqCur = max(0, len(m.lobby.Requests)-1)
		}
		if msg.err == nil {
			m.st, m.log = msg.st, msg.log
			if msg.assets != nil {
				m.assets = msg.assets
			}
			if m.histCur >= len(m.log) {
				m.histCur = max(0, len(m.log)-1)
			}
		}
		return m, nil

	case actionMsg:
		m.busy = ""
		if msg.err != nil {
			m.msg, m.msgWarn = []string{"Blad: " + msg.err.Error()}, true
		} else {
			m.msg, m.msgWarn = msg.lines, msg.warn
		}
		if msg.pull != nil && len(msg.pull.Conflicts) > 0 {
			m.conflict = &conflictState{with: msg.pull.ConflictWith, list: msg.pull.Conflicts, choice: map[string]als.Resolution{}}
		}
		m.busy = "odswiezanie"
		return m, m.refreshCmd(true)

	case tea.KeyMsg:
		if m.conflict != nil {
			return m.updateConflict(msg)
		}
		return m.updateMain(msg)
	}
	return m, nil
}

func (m Model) updateMain(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "tab", "right", "l":
		m.tab = (m.tab + 1) % tab(len(tabNames))
	case "shift+tab", "left", "h":
		m.tab = (m.tab + tab(len(tabNames)) - 1) % tab(len(tabNames))
	case "1", "2", "3", "4":
		m.tab = tab(k.String()[0] - '1')
	case "up", "k":
		if m.tab == tabHistory && m.histCur > 0 {
			m.histCur--
		}
		if m.tab == tabLobby && m.reqCur > 0 {
			m.reqCur--
		}
	case "down", "j":
		if m.tab == tabHistory && m.histCur < len(m.log)-1 {
			m.histCur++
		}
		if m.tab == tabLobby && m.lobby != nil && m.reqCur < len(m.lobby.Requests)-1 {
			m.reqCur++
		}
	case "esc":
		m.invite = ""
	case "i":
		if m.net != nil {
			m.tab = tabLobby
			m.invite = m.net.Invite(m.p.Me.Name)
			m.copied = clipboard(m.invite) == nil
		}
		return m, nil
	}
	if m.busy != "" {
		return m, nil
	}
	switch k.String() {
	case "c":
		m.busy = "zapisywanie wersji"
		return m, m.commitCmd()
	case "p":
		m.busy = "pobieranie i scalanie"
		return m, m.pullCmd(nil)
	case "r":
		m.busy = "odswiezanie"
		return m, m.refreshCmd(true)
	case "a", "n":
		if m.tab != tabLobby || m.net == nil || m.lobby == nil || len(m.lobby.Requests) == 0 {
			return m, nil
		}
		r := m.lobby.Requests[m.reqCur]
		if k.String() == "a" {
			if m.lobby.Full() {
				m.msg, m.msgWarn = []string{fmt.Sprintf("Lobby pelne (%d osob) - nie mozna przyjac %s.", syncthing.MaxMembers, r.Name)}, true
				return m, nil
			}
			m.busy = "przyjmowanie " + r.Name
			return m, m.lobbyCmd(r, true)
		}
		m.busy = "odrzucanie " + r.Name
		return m, m.lobbyCmd(r, false)
	}
	return m, nil
}

func (m Model) lobbyCmd(r syncthing.Request, accept bool) tea.Cmd {
	net := m.net
	return func() tea.Msg {
		if accept {
			if err := net.Accept(r); err != nil {
				return actionMsg{err: err}
			}
			return actionMsg{lines: []string{r.Name + " przyjety - dostanie projekt, gdy tylko sie polaczy."}}
		}
		if err := net.Reject(r); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{lines: []string{"Odrzucono prosbe od " + r.Name + "."}}
	}
}

func (m Model) updateConflict(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	cs := m.conflict
	c := cs.list[cs.cur]
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.conflict = nil
		m.msg, m.msgWarn = []string{"Scalanie przerwane - nic nie zostalo zmienione. Pobierz ponownie (p), zeby wrocic do wyboru."}, true
		return m, nil
	case "up", "k", "shift+tab":
		if cs.cur > 0 {
			cs.cur--
		}
	case "down", "j", "tab":
		if cs.cur < len(cs.list)-1 {
			cs.cur++
		}
	case "o":
		cs.choice[c.ID] = als.Ours
		cs.advance()
	case "t":
		cs.choice[c.ID] = als.Theirs
		cs.advance()
	case "b":
		if c.CanBoth {
			cs.choice[c.ID] = als.Both
			cs.advance()
		}
	case "enter":
		if len(cs.choice) == len(cs.list) {
			res := cs.choice
			m.conflict = nil
			m.busy = "scalanie"
			return m, m.pullCmd(res)
		}
	}
	return m, nil
}

// advance: po wyborze przejdz do pierwszego konfliktu bez wyboru.
func (cs *conflictState) advance() {
	for i := range cs.list {
		j := (cs.cur + 1 + i) % len(cs.list)
		if _, ok := cs.choice[cs.list[j].ID]; !ok {
			cs.cur = j
			return
		}
	}
}

// --- View -------------------------------------------------------------------------------

var (
	cAccent = lipgloss.AdaptiveColor{Light: "#5A3FC0", Dark: "#A78BFA"}
	cDim    = lipgloss.AdaptiveColor{Light: "#6B6B6B", Dark: "#8A8A8A"}
	cOK     = lipgloss.AdaptiveColor{Light: "#1F7A3A", Dark: "#4ADE80"}
	cWarn   = lipgloss.AdaptiveColor{Light: "#9A6200", Dark: "#FBBF24"}
	cBad    = lipgloss.AdaptiveColor{Light: "#B42318", Dark: "#F87171"}

	sTitle    = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sDim      = lipgloss.NewStyle().Foreground(cDim)
	sBold     = lipgloss.NewStyle().Bold(true)
	sTabOn    = lipgloss.NewStyle().Bold(true).Foreground(cAccent).Underline(true)
	sTabOff   = lipgloss.NewStyle().Foreground(cDim)
	sSection  = lipgloss.NewStyle().Bold(true).MarginTop(1)
	sBox      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cDim).Padding(0, 1)
	sWarnBox  = sBox.BorderForeground(cWarn)
	sModalBox = lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).BorderForeground(cWarn).Padding(0, 1)
	sKey      = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
)

func relStyle(r store.Relation) lipgloss.Style {
	switch r {
	case store.RelSame:
		return lipgloss.NewStyle().Foreground(cOK)
	case store.RelAhead, store.RelBehind:
		return lipgloss.NewStyle().Foreground(cWarn)
	case store.RelDiverged:
		return lipgloss.NewStyle().Foreground(cBad)
	}
	return sDim
}

func (m Model) View() string {
	w := max(40, m.width)
	var b strings.Builder

	head := sTitle.Render("ableton-collab") + sDim.Render("  ·  "+m.p.Meta.Set+"  ·  "+m.p.Me.Name)
	if m.busy != "" && m.busy != "odswiezanie" {
		head += "  " + lipgloss.NewStyle().Foreground(cWarn).Render("⟳ "+m.busy+"...")
	}
	b.WriteString(head + "\n")
	var tabs []string
	for i, n := range tabNames {
		label := fmt.Sprintf("%d %s", i+1, n)
		if tab(i) == m.tab {
			tabs = append(tabs, sTabOn.Render(label))
		} else {
			tabs = append(tabs, sTabOff.Render(label))
		}
	}
	b.WriteString(strings.Join(tabs, "   ") + "\n")

	if m.conflict != nil {
		b.WriteString(m.viewConflict(w))
		return b.String()
	}
	if m.err != nil {
		b.WriteString(sWarnBox.Width(w-4).Render("Blad: "+m.err.Error()) + "\n")
	}
	switch m.tab {
	case tabStatus:
		b.WriteString(m.viewStatus(w))
	case tabLobby:
		b.WriteString(m.viewLobby(w))
	case tabHistory:
		b.WriteString(m.viewHistory(w))
	case tabProject:
		b.WriteString(m.viewProject(w))
	}
	if len(m.msg) > 0 && (m.tab == tabStatus || m.tab == tabLobby) {
		box := sBox
		if m.msgWarn {
			box = sWarnBox
		}
		b.WriteString("\n" + box.Width(w-4).Render(strings.Join(clip(m.msg, w-8, 14), "\n")) + "\n")
	}
	b.WriteString("\n" + m.footer())
	return b.String()
}

func (m Model) footer() string {
	k := func(key, desc string) string { return sKey.Render(key) + sDim.Render(" "+desc) }
	parts := []string{k("c", "zapisz wersje"), k("p", "pobierz i scal"), k("r", "odswiez")}
	if m.tab == tabHistory {
		parts = append(parts, k("↑↓", "wybor"))
	}
	if m.net != nil {
		parts = append(parts, k("i", "zapros"))
	}
	if m.tab == tabLobby && m.lobby != nil && len(m.lobby.Requests) > 0 {
		parts = append(parts, k("a", "przyjmij"), k("n", "odrzuc"))
	}
	parts = append(parts, k("tab", "zakladki"), k("q", "wyjscie"))
	return strings.Join(parts, sDim.Render("  ·  "))
}

func (m Model) viewStatus(w int) string {
	var b strings.Builder
	if m.st == nil {
		return sDim.Render("\nwczytywanie...") + "\n"
	}
	b.WriteString(sSection.Render("Twoja wersja") + "\n")
	if m.st.Head == nil {
		b.WriteString("  brak - pobierz projekt (p)\n")
	} else {
		v := m.st.Head
		b.WriteString(fmt.Sprintf("  %s  %s  %s\n", sBold.Render(v.Short()), v.Author, sDim.Render(v.Time.Local().Format("2006-01-02 15:04"))))
	}
	if n := len(m.st.Changes); n > 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(cWarn).Render(fmt.Sprintf("  %d zmian(y) w secie nie zapisanych jako wersja (c):", n)) + "\n")
		var lines []string
		for _, c := range m.st.Changes {
			lines = append(lines, c.String())
		}
		for _, l := range clip(lines, w-6, 6) {
			b.WriteString("    " + sDim.Render(l) + "\n")
		}
	} else if m.st.Head != nil {
		b.WriteString(sDim.Render("  set zgodny z wersja") + "\n")
	}

	b.WriteString(sSection.Render("Uczestnicy") + "\n")
	if len(m.st.Peers) == 0 {
		b.WriteString(sDim.Render("  nikt jeszcze nie dolaczyl - udostepnij folder projektu") + "\n")
	}
	for _, ps := range m.st.Peers {
		ver := ""
		if ps.Head != nil {
			ver = sDim.Render(fmt.Sprintf("  %s · %s", ps.Head.Short(), ps.Head.Time.Local().Format("2006-01-02 15:04")))
		}
		b.WriteString(fmt.Sprintf("  %s %-20s %s%s\n", m.onlineDot(ps.Device.Peer), trunc(ps.Device.Name, 20), relStyle(ps.Relation).Render(ps.Relation.String()), ver))
	}
	if m.lobby != nil && len(m.lobby.Requests) > 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(cWarn).Render(fmt.Sprintf("  %d prosb(y) o dolaczenie - zakladka Lobby (2)", len(m.lobby.Requests))) + "\n")
	}
	return b.String()
}

func (m Model) viewHistory(w int) string {
	var b strings.Builder
	if len(m.log) == 0 {
		return sDim.Render("\nbrak wersji") + "\n"
	}
	head := ""
	if m.st != nil && m.st.Head != nil {
		head = m.st.Head.ID
	}
	listH := max(5, min(len(m.log), (m.height-12)/2))
	start := max(0, min(m.histCur-listH/2, len(m.log)-listH))
	b.WriteString("\n")
	for i := start; i < min(len(m.log), start+listH); i++ {
		v := m.log[i]
		mark := "  "
		if v.ID == head {
			mark = lipgloss.NewStyle().Foreground(cOK).Render("● ")
		}
		kind := ""
		if len(v.Parents) > 1 {
			kind = lipgloss.NewStyle().Foreground(cAccent).Render(" scalenie")
		}
		line := fmt.Sprintf("%s%s  %s  %-18s%s", mark, v.Short(), v.Time.Local().Format("2006-01-02 15:04"), trunc(v.Author, 18), kind)
		if i == m.histCur {
			line = lipgloss.NewStyle().Reverse(true).Render(line)
		}
		b.WriteString(line + "\n")
	}
	v := m.log[m.histCur]
	detail := []string{sBold.Render("Wersja "+v.Short()) + sDim.Render("  "+v.Author+" · "+v.Time.Local().Format("2006-01-02 15:04:05"))}
	if len(v.Parents) > 0 {
		var ps []string
		for _, p := range v.Parents {
			ps = append(ps, store.Short(p))
		}
		detail = append(detail, sDim.Render("rodzice: "+strings.Join(ps, ", ")))
	}
	detail = append(detail, "")
	detail = append(detail, clip(v.Summary, w-8, max(4, m.height-listH-12))...)
	b.WriteString("\n" + sBox.Width(w-4).Render(strings.Join(detail, "\n")) + "\n")
	return b.String()
}

func (m Model) viewProject(w int) string {
	var b strings.Builder
	row := func(k, v string) { b.WriteString(fmt.Sprintf("  %-14s %s\n", sDim.Render(k), v)) }
	b.WriteString(sSection.Render("Projekt") + "\n")
	row("folder", m.p.Dir)
	row("set", m.p.Meta.Set)
	row("id projektu", m.p.Meta.ID)
	row("ten komputer", m.p.Me.Name+sDim.Render("  ("+m.p.Me.ID+")"))
	row("tryb", "reczny"+sDim.Render("  (automatycznie po zapisie w Live - M5)"))
	b.WriteString(sSection.Render("Udostepnianie") + "\n")
	if m.net == nil {
		b.WriteString(sDim.Render("  Bez sieci (-offline): udostepnij caly folder projektu (z .abcollab/) dowolnym syncem.") + "\n")
	} else {
		b.WriteString(sDim.Render("  Syncthing "+syncthing.Version+": zapraszanie w zakladce Lobby (i). Plik .als nie jest") + "\n")
		b.WriteString(sDim.Render("  wysylany wprost - zmiany ida jako wersje i sa scalane.") + "\n")
	}
	b.WriteString(sSection.Render("Co musi miec kazdy uczestnik") + "\n")
	if len(m.assets) == 0 {
		b.WriteString(sDim.Render("  nic poza folderem projektu") + "\n")
	}
	var lines []string
	for _, a := range m.assets {
		lines = append(lines, a.Message)
	}
	for _, l := range clip(lines, w-6, 10) {
		b.WriteString("  " + l + "\n")
	}
	return b.String()
}

func (m Model) viewConflict(w int) string {
	cs := m.conflict
	c := cs.list[cs.cur]
	var b strings.Builder
	b.WriteString(sBold.Render(fmt.Sprintf("Konflikt z %s  (%d/%d)", cs.with.Name, cs.cur+1, len(cs.list))) + "\n")
	b.WriteString(sDim.Render("Obie strony zmienily ten sam element. Wybierz, co zachowac:") + "\n\n")
	b.WriteString(lipgloss.NewStyle().Foreground(cWarn).Bold(true).Render(trunc(c.UnitLabel, w-8)) + "\n\n")
	side := func(title string, ch []als.Change) {
		b.WriteString(sBold.Render(title) + "\n")
		var lines []string
		for _, x := range ch {
			lines = append(lines, x.String())
		}
		for _, l := range clip(lines, w-10, 6) {
			b.WriteString("  " + sDim.Render(l) + "\n")
		}
	}
	side("Twoje zmiany:", c.Ours)
	b.WriteString("\n")
	side(cs.with.Name+":", c.Theirs)
	b.WriteString("\n")
	opt := func(key, label string, r als.Resolution) string {
		s := "[" + key + "] " + label
		if cs.choice[c.ID] == r {
			return lipgloss.NewStyle().Reverse(true).Bold(true).Render(" " + s + " ")
		}
		return sKey.Render("["+key+"]") + " " + label
	}
	opts := []string{opt("o", "zachowaj moje", als.Ours), opt("t", "zachowaj "+cs.with.Name, als.Theirs)}
	if c.CanBoth {
		opts = append(opts, opt("b", "zachowaj oba (drugi klip w wolny slot)", als.Both))
	}
	b.WriteString(strings.Join(opts, "    ") + "\n")

	var list []string
	for i, x := range cs.list {
		mark := sDim.Render("○")
		if r, ok := cs.choice[x.ID]; ok {
			mark = lipgloss.NewStyle().Foreground(cOK).Render("● " + r.String())
		}
		line := fmt.Sprintf("%s  %s", mark, trunc(x.UnitLabel, w-24))
		if i == cs.cur {
			line = sBold.Render("› ") + line
		} else {
			line = "  " + line
		}
		list = append(list, line)
	}
	if len(cs.list) > 1 {
		b.WriteString("\n" + strings.Join(list, "\n") + "\n")
	}
	ready := len(cs.choice) == len(cs.list)
	hint := sDim.Render(fmt.Sprintf("wybrano %d/%d", len(cs.choice), len(cs.list)))
	if ready {
		hint = lipgloss.NewStyle().Foreground(cOK).Render("gotowe - ") + sKey.Render("enter") + sDim.Render(" scal")
	}
	b.WriteString("\n" + hint + sDim.Render("  ·  ") + sKey.Render("↑↓") + sDim.Render(" konflikty  ·  ") + sKey.Render("esc") + sDim.Render(" anuluj"))
	return sModalBox.Width(w-4).Render(b.String()) + "\n"
}

// --- pomocnicze -------------------------------------------------------------------------

func trunc(s string, n int) string {
	if n <= 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// clip przycina linie do szerokosci i liczby wierszy.
func clip(lines []string, width, maxLines int) []string {
	var out []string
	for i, l := range lines {
		if i == maxLines {
			out = append(out, fmt.Sprintf("… i %d wiecej", len(lines)-maxLines))
			break
		}
		out = append(out, trunc(l, width))
	}
	return out
}

package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/c-airr/ableton-collab/als"
	"github.com/c-airr/ableton-collab/internal/live"
	"github.com/c-airr/ableton-collab/store"
	"github.com/c-airr/ableton-collab/transport/syncthing"

	tea "github.com/charmbracelet/bubbletea"
)

const refreshing = "refreshing"

// session: otwarta sesja i wszystko, co o niej wiemy.
type session struct {
	p   *store.Project
	net Network
	now func() time.Time

	st       *store.Status
	versions []*store.Version
	lobby    *syncthing.Lobby
	lobbyErr error
	err      error
	tree     tree
	log      []logLine
	logOff   int // przewiniecie logu od dolu
	histCur  int
	peerCur  int
	busy     string
	heads    map[string]string // ostatnio widziana wersja innych - do logu "X edited"
	reqSeen  map[string]bool
	ticks    int

	// praca na zywo
	live      LiveSync
	liveSt    live.Status
	liveQueue []live.Note  // konflikty czekajace na okno
	reload    []als.Change // zmiany innych, ktore wymagaja przeladowania w Live
	caught    string       // para wersji (wczytana>head), ktorej zmiany poszly juz do Live'a
	dirty     bool         // w Live sa zmiany niezapisane do pliku
	lastLocal time.Time    // ostatnia moja zmiana w Live
	lastTry   time.Time    // ostatnia proba auto-zapisu
	mtime     time.Time    // data pliku seta przy ostatnim sprawdzeniu
	saveErr   string
}

// liveOn: Live ma otwarty set tej sesji i skrypt dziala - zmiany ida na zywo,
// plik seta nie jest podmieniany przy pobieraniu.
func (s *session) liveOn() bool { return s.live != nil && s.liveSt.Matched }

type logLine struct {
	at    time.Time
	level level
	text  string
}

func newSession(p *store.Project, net Network, now func() time.Time) *session {
	return &session{p: p, net: net, now: now, tree: newTree(p.Dir), reqSeen: map[string]bool{}}
}

func sessionName(p *store.Project) string { return filepath.Base(p.Dir) }

func (s *session) logf(lv level, format string, args ...any) {
	s.log = append(s.log, logLine{at: s.now(), level: lv, text: fmt.Sprintf(format, args...)})
	if len(s.log) > 500 {
		s.log = s.log[len(s.log)-500:]
	}
	s.logOff = 0
}

func (s *session) isHost() bool {
	return s.p.Meta.Host != "" && s.p.Meta.Host == s.p.Me.Peer
}

// --- komunikaty sesji -------------------------------------------------------------------

type sessionMsg interface{ sessionID() int }

type refreshMsg struct {
	sid      int
	st       *store.Status
	versions []*store.Version
	lobby    *syncthing.Lobby
	lobbyErr error
	tree     []treeNode
	reload   []als.Change
	mtime    time.Time
	err      error
	// zmiany z nowszej wersji do naniesienia w Live (pair: wczytana>head)
	pair    string
	catchup []live.Catch
}

type liveMsg struct {
	sid  int
	note live.Note
	ok   bool
}

type savedKeyMsg struct {
	sid  int
	sent bool
	err  error
}

func (m liveMsg) sessionID() int     { return m.sid }
func (m savedKeyMsg) sessionID() int { return m.sid }

type eventsMsg struct {
	sid  int
	evs  []syncthing.Event
	next int
	err  error
}

type actionMsg struct {
	sid   int
	lines []logLine
	pull  *store.PullResult
	err   error
}

func (m refreshMsg) sessionID() int { return m.sid }
func (m eventsMsg) sessionID() int  { return m.sid }
func (m actionMsg) sessionID() int  { return m.sid }

func (s *session) refreshCmd(sid int) tea.Cmd {
	p, net, dir, liveOn, caught := s.p, s.net, s.p.Dir, s.liveOn(), s.caught
	return func() tea.Msg {
		r := refreshMsg{sid: sid}
		if net != nil {
			r.lobby, r.lobbyErr = net.Lobby()
		}
		r.tree = scanTree(dir, 5000)
		if fi, err := os.Stat(p.WorkingPath()); err == nil {
			r.mtime = fi.ModTime()
		}
		if liveOn {
			r.pair, r.catchup = catchupItems(p, caught)
		}
		if pr, err := p.PendingReload(); err == nil {
			for _, c := range pr {
				// zmiany klipow MIDI, panoramy itp. wchodza do Live'a same (na
				// zywo albo z wersji) - ale tylko, gdy Live ma ten set
				if !liveOn || !liveSyncable(c) {
					r.reload = append(r.reload, c)
				}
			}
		}
		st, err := p.Status()
		if err != nil {
			r.err = err
			return r
		}
		vs, err := p.Versions()
		if err != nil {
			r.err = err
			return r
		}
		r.st, r.versions = st, store.Log(vs)
		return r
	}
}

// eventsCmd: long-poll zdarzen Syncthinga (pliki, polaczenia, prosby).
func (s *session) eventsCmd(sid, since int) tea.Cmd {
	net := s.net
	return func() tea.Msg {
		evs, next, err := net.Events(since, 20*time.Second)
		if err != nil {
			time.Sleep(3 * time.Second) // Syncthing chwilowo niedostepny
		}
		return eventsMsg{sid: sid, evs: evs, next: next, err: err}
	}
}

func (s *session) handle(a *App, msg sessionMsg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case refreshMsg:
		if s.busy == refreshing {
			s.busy = ""
		}
		s.err = m.err
		s.lobby, s.lobbyErr = m.lobby, m.lobbyErr
		s.tree.set(m.tree)
		if m.err == nil {
			s.noticePeers(m.st, m.versions)
			s.st, s.versions = m.st, m.versions
			if s.histCur >= len(s.versions) {
				s.histCur = max(0, len(s.versions)-1)
			}
		}
		s.noticeRequests()
		s.clampPeer()
		if len(m.reload) > 0 && len(s.reload) == 0 {
			s.logf(lWarn, "Reload the set to get %d change(s) that can't come in live (e.g. %s)", len(m.reload), shortChange(m.reload[0].String()))
		}
		s.reload = m.reload
		if m.pair != "" && m.pair != s.caught && s.liveOn() {
			s.caught = m.pair
			if len(m.catchup) > 0 {
				s.live.Catchup(m.catchup)
			}
		}
		saved := !s.mtime.IsZero() && !m.mtime.IsZero() && !m.mtime.Equal(s.mtime)
		if !m.mtime.IsZero() {
			s.mtime = m.mtime
		}
		if saved && s.live != nil {
			s.live.Recheck() // po zapisie plik zgadza sie z Live (np. nowe sciezki)
		}
		return *a, s.auto(a, saved)

	case eventsMsg:
		for _, e := range m.evs {
			s.logEvent(e)
		}
		return *a, s.eventsCmd(m.sid, m.next)

	case liveMsg:
		if !m.ok {
			return *a, nil // silnik zamkniety
		}
		s.onLive(a, m.note)
		return *a, s.liveCmd(m.sid)

	case savedKeyMsg:
		switch {
		case m.err != nil:
			if s.saveErr != m.err.Error() {
				s.saveErr = m.err.Error()
				s.logf(lWarn, "Auto-save: %s", m.err.Error())
			}
		case m.sent:
			s.dirty, s.saveErr = false, ""
			s.logf(lInfo, "Saved in Live (Ctrl+S)")
		}
		return *a, nil

	case actionMsg:
		s.busy = ""
		for _, l := range m.lines {
			s.log = append(s.log, l)
		}
		s.logOff = 0
		if m.err != nil {
			s.logf(lErr, "%s", m.err.Error())
			a.say(lErr, m.err.Error())
		}
		if m.pull != nil && len(m.pull.Conflicts) > 0 && a.dlg == nil {
			liveMode := s.liveOn()
			a.dlg = newConflictDialog(a, m.pull, nil, func(a *App, res map[string]als.Resolution) (dialog, tea.Cmd) {
				a.ses.busy = "merging"
				return nil, a.ses.pullCmd(a.sid, res, liveMode)
			})
		}
		s.busy = refreshing
		return *a, s.refreshCmd(m.sid)
	}
	return *a, nil
}

// noticePeers: ktos zapisal nowa wersje - wpis "X edited ..." do logu.
func (s *session) noticePeers(st *store.Status, vs []*store.Version) {
	if st == nil {
		return
	}
	first := s.heads == nil
	if first {
		s.heads = map[string]string{}
	}
	for _, ps := range st.Peers {
		if ps.Head == nil {
			continue
		}
		prev := s.heads[ps.Device.ID]
		s.heads[ps.Device.ID] = ps.Head.ID
		if first || prev == ps.Head.ID {
			continue
		}
		what := "a new version"
		if len(ps.Head.Summary) > 0 {
			what = shortChange(ps.Head.Summary[0])
			if n := len(ps.Head.Summary); n > 1 {
				what += fmt.Sprintf(" (+%d more)", n-1)
			}
		}
		s.logf(lWarn, "%s edited %s: %s", ps.Device.Name, s.p.SetName(), what)
	}
}

func (s *session) noticeRequests() {
	if s.lobby == nil {
		return
	}
	for _, r := range s.lobby.Requests {
		if !s.reqSeen[r.ID] {
			s.reqSeen[r.ID] = true
			s.logf(lWarn, "%s (%s) wants to join", r.Name, syncthing.ShortID(r.ID))
		}
	}
}

// shortChange: "modified  MidiTrack "Hats" › slot 0 › MidiClip: Velocity 70 → 12"
// -> "Hats › slot 0 › MidiClip".
func shortChange(s string) string {
	if f := strings.Fields(s); len(f) > 1 {
		s = strings.TrimSpace(strings.TrimPrefix(s, f[0]))
	}
	if i := strings.Index(s, ": "); i > 0 {
		s = s[:i]
	}
	for _, t := range []string{"MidiTrack ", "AudioTrack ", "GroupTrack ", "ReturnTrack "} {
		s = strings.TrimPrefix(s, t)
	}
	return strings.ReplaceAll(s, `"`, "")
}

func (s *session) logEvent(e syncthing.Event) {
	switch e.Kind {
	case "received":
		s.logf(lFile, "↓ %s received", e.File)
	case "sending":
		s.logf(lFile, "↑ %s → sending to others", e.File)
	case "deleted":
		s.logf(lFile, "✕ %s deleted", e.File)
	case "error":
		s.logf(lErr, "%s failed to sync", e.File)
	case "connected":
		s.logf(lOK, "%s connected", e.Who)
	case "disconnected":
		s.logf(lInfo, "%s disconnected", e.Who)
	}
}

// --- akcje -------------------------------------------------------------------------------

func (s *session) commitCmd(sid int) tea.Cmd {
	p, now := s.p, s.now
	return func() tea.Msg {
		at := now()
		if p.Head() == "" {
			return actionMsg{sid: sid, lines: []logLine{{at, lWarn, "You don't have this session's set yet - Get changes first"}}}
		}
		v, err := p.Commit()
		if err != nil {
			return actionMsg{sid: sid, err: err}
		}
		if v == nil {
			return actionMsg{sid: sid, lines: []logLine{{at, lInfo, "Nothing to save - the set has no changes"}}}
		}
		return actionMsg{sid: sid, lines: []logLine{{at, lOK, fmt.Sprintf("You saved version %s (%d change(s))", v.Short(), len(v.Summary))}}}
	}
}

func (s *session) pullCmd(sid int, res map[string]als.Resolution, liveMode bool) tea.Cmd {
	p, now := s.p, s.now
	return func() tea.Msg {
		pull := p.Pull
		if liveMode {
			pull = p.PullLive
		}
		r, err := pull(res)
		if err != nil {
			return actionMsg{sid: sid, err: err}
		}
		return actionMsg{sid: sid, pull: r, lines: pullLines(p, r, now())}
	}
}

func pullLines(p *store.Project, r *store.PullResult, at time.Time) []logLine {
	var out []logLine
	add := func(lv level, f string, args ...any) { out = append(out, logLine{at, lv, fmt.Sprintf(f, args...)}) }
	if r.Committed != nil {
		add(lOK, "Saved your changes as version %s", r.Committed.Short())
	}
	if r.Backup != "" {
		add(lInfo, "Your previous set was copied to %s", filepath.Base(r.Backup))
	}
	for _, d := range r.FastForwarded {
		add(lOK, "Got changes from %s", d.Name)
	}
	for _, v := range r.Merged {
		add(lOK, "Merged changes (version %s)", v.Short())
	}
	for _, w := range r.Warnings {
		add(lWarn, "%s", w.Message)
	}
	for _, d := range r.Incomplete {
		add(lInfo, "Changes from %s are still arriving", d.Name)
	}
	if len(r.Conflicts) > 0 {
		add(lErr, "%d conflict(s) with %s - choose what to keep", len(r.Conflicts), r.ConflictWith.Name)
	}
	if r.WorkingChanged() && !r.Live {
		add(lWarn, "%s was updated - reopen it in Live and don't save the old one", p.SetName())
	}
	if len(out) == 0 {
		add(lInfo, "Everything is up to date")
	}
	return out
}

func (s *session) lobbyCmd(sid int, what string, r *syncthing.Request, m *syncthing.Member) tea.Cmd {
	net, now := s.net, s.now
	return func() tea.Msg {
		var err error
		var text string
		switch what {
		case "accept":
			err, text = net.Accept(*r), r.Name+" accepted - they get the session as soon as they connect"
		case "reject":
			err, text = net.Reject(*r), "Rejected "+r.Name
		case "kick":
			err, text = net.Kick(*m), "Kicked "+m.Name
		}
		if err != nil {
			return actionMsg{sid: sid, err: err}
		}
		return actionMsg{sid: sid, lines: []logLine{{now(), lOK, text}}}
	}
}

func (s *session) deleteCmd(sid int, path string) tea.Cmd {
	dir, now := s.p.Dir, s.now
	return func() tea.Msg {
		if err := os.RemoveAll(path); err != nil {
			return actionMsg{sid: sid, err: err}
		}
		return actionMsg{sid: sid, lines: []logLine{{now(), lOK, "You deleted " + relName(dir, path)}}}
	}
}

func relName(dir, path string) string {
	if r, err := filepath.Rel(dir, path); err == nil {
		return filepath.ToSlash(r)
	}
	return path
}

// --- ludzie ------------------------------------------------------------------------------

// people: wiersze panelu (bez mnie): najpierw uczestnicy, potem prosby.
func (s *session) peopleRows() (members []syncthing.Member, reqs []syncthing.Request) {
	if s.lobby == nil {
		return nil, nil
	}
	return s.lobby.Members, s.lobby.Requests
}

func (s *session) clampPeer() {
	m, r := s.peopleRows()
	if n := len(m) + len(r); s.peerCur >= n {
		s.peerCur = max(0, n-1)
	}
}

func (s *session) selMember() *syncthing.Member {
	m, _ := s.peopleRows()
	if s.peerCur < len(m) {
		return &m[s.peerCur]
	}
	return nil
}

func (s *session) selRequest() *syncthing.Request {
	m, r := s.peopleRows()
	if i := s.peerCur - len(m); i >= 0 && i < len(r) {
		return &r[i]
	}
	return nil
}

func (s *session) peopleKey(k string) bool {
	m, r := s.peopleRows()
	switch k {
	case "up":
		if s.peerCur > 0 {
			s.peerCur--
		}
	case "down":
		if s.peerCur < len(m)+len(r)-1 {
			s.peerCur++
		}
	default:
		return false
	}
	return true
}

// peerStatus: stan wersji uczestnika po ID w transporcie.
func (s *session) peerStatus(peer string) *store.PeerStatus {
	if s.st == nil {
		return nil
	}
	for i, ps := range s.st.Peers {
		if ps.Device.Peer == peer {
			return &s.st.Peers[i]
		}
	}
	return nil
}

func (s *session) centerKey(mode centerMode, k string) bool {
	switch {
	case mode == cHistory && k == "up":
		if s.histCur > 0 {
			s.histCur--
		}
	case mode == cHistory && k == "down":
		if s.histCur < len(s.versions)-1 {
			s.histCur++
		}
	case mode == cLog && k == "up":
		s.logOff++
	case mode == cLog && k == "down":
		if s.logOff > 0 {
			s.logOff--
		}
	default:
		return false
	}
	return true
}

package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/c-airr/ableton-collab/als"
	"github.com/c-airr/ableton-collab/internal/live"
	"github.com/c-airr/ableton-collab/store"

	tea "github.com/charmbracelet/bubbletea"
)

// Praca na zywo i tryb automatyczny:
//   - zmiany z Live'a ida od razu przez silnik (internal/live), zmiany innych
//     wchodza do otwartego Live'a bez przeladowania;
//   - zapis seta w Live (Ctrl+S, recznie albo przez auto-zapis) = nowa wersja;
//   - nowe wersje innych sa pobierane bez podmiany pliku (PullLive) - to, czego
//     nie da sie przeniesc na zywo, czeka na Reload.

const autoSaveIdle = 4 * time.Second

// liveCmd czeka na nastepna wiadomosc od silnika.
func (s *session) liveCmd(sid int) tea.Cmd {
	ls := s.live
	return func() tea.Msg {
		n, ok := <-ls.Notes()
		return liveMsg{sid: sid, note: n, ok: ok}
	}
}

func (s *session) onLive(a *App, n live.Note) {
	switch n.Kind {
	case "status":
		prev := s.liveSt
		s.liveSt = s.live.Status()
		switch {
		case s.liveSt.Matched && !prev.Matched:
			s.logf(lOK, "Live connected - changes now go live")
		case !s.liveSt.Matched && s.liveSt.Detail != prev.Detail && s.liveSt.Detail != "":
			s.logf(lWarn, "Live: %s", s.liveSt.Detail)
		}
	case "log":
		s.logf(lOK, "%s", n.Text)
	case "local":
		s.dirty, s.lastLocal = true, s.now()
		s.logf(lFile, "↑ %s", n.Text)
	case "structure":
		s.dirty, s.lastLocal = true, s.now()
		s.logf(lInfo, "Live: tracks/devices changed - goes out with the next save")
	case "reload":
		s.logf(lWarn, "%s", n.Text)
	case "conflict":
		s.liveQueue = append(s.liveQueue, n)
	case "resolved":
		if d, ok := a.dlg.(*liveConflictDialog); ok && d.n.Key == n.Key {
			a.dlg = nil
		}
		var keep []live.Note
		for _, q := range s.liveQueue {
			if q.Key != n.Key {
				keep = append(keep, q)
			}
		}
		s.liveQueue = keep
	}
	if a.dlg == nil && len(s.liveQueue) > 0 {
		a.dlg = &liveConflictDialog{n: s.liveQueue[0]}
		s.liveQueue = s.liveQueue[1:]
	}
}

// auto: krok trybu automatycznego (po kazdym odswiezeniu). saved - plik seta
// zmienil sie od ostatniego sprawdzenia (Live zapisal).
func (s *session) auto(a *App, saved bool) tea.Cmd {
	if s.busy != "" {
		return nil
	}
	set := a.settings
	if saved && set.AutoSync {
		s.dirty = false
		s.busy = "saving"
		return s.autoSaveCmd(a.sid, !s.fileFree())
	}
	// nowe wersje innych - zawsze same, bez Get changes
	if set.AutoSync && s.st != nil {
		for _, ps := range s.st.Peers {
			if ps.Relation == store.RelAhead || ps.Relation == store.RelDiverged {
				s.logf(lFile, "↓ New version from %s - getting it", ps.Device.Name)
				s.busy = "getting changes"
				return s.pullCmd(a.sid, nil, !s.fileFree())
			}
		}
	}
	now := s.now()
	if set.AutoSave && s.liveOn() && s.dirty && now.Sub(s.lastLocal) >= autoSaveIdle && now.Sub(s.lastTry) >= 3*time.Second {
		s.lastTry = now
		be, sid := a.be, a.sid
		return func() tea.Msg {
			sent, err := be.SaveInLive()
			return savedKeyMsg{sid: sid, sent: sent, err: err}
		}
	}
	return nil
}

// fileFree: plik seta mozna podmienic - Live jest polaczony i ma otwarty
// INNY set. Gdy Live ma ten set (praca na zywo) albo nie wiemy, co ma
// otwarte (skrypt nie odpowiada), pliku nie ruszamy: zmiany wchodza na zywo
// albo czekaja na Open in Live / Reload.
func (s *session) fileFree() bool {
	return s.live != nil && s.liveSt.Connected && !s.liveSt.Matched
}

// autoSaveCmd: Live zapisal set - nowa wersja i od razu zmiany innych.
func (s *session) autoSaveCmd(sid int, liveMode bool) tea.Cmd {
	p, now := s.p, s.now
	return func() tea.Msg {
		at := now()
		if p.Head() == "" {
			return actionMsg{sid: sid}
		}
		pull := p.Pull
		if liveMode {
			pull = p.PullLive
		}
		r, err := pull(nil)
		if err != nil {
			return actionMsg{sid: sid, err: err}
		}
		var lines []logLine
		if r.Committed != nil {
			lines = append(lines, logLine{at, lOK, fmt.Sprintf("↑ Live saved the set - sent as version %s", r.Committed.Short())})
		}
		r.Committed = nil
		if r.WorkingChanged() || len(r.Conflicts) > 0 || len(r.Incomplete) > 0 {
			lines = append(lines, pullLines(p, r, at)...)
		}
		return actionMsg{sid: sid, pull: r, lines: lines}
	}
}

// openInLiveCmd: najpierw najnowsza wersja do pliku (Live jeszcze go nie ma
// otwartego, a zmiany z czasu, gdy byl zamkniety, nie przyszly na zywo), potem
// otwarcie seta w Live.
func (s *session) openInLiveCmd(sid int, be Backend) tea.Cmd {
	p, now := s.p, s.now
	return func() tea.Msg {
		at := now()
		var lines []logLine
		r, err := p.Pull(nil)
		if err != nil {
			return actionMsg{sid: sid, err: err}
		}
		if r.WorkingChanged() {
			lines = append(lines, logLine{at, lOK, "Got the latest version before opening"})
		}
		if len(r.Conflicts) > 0 {
			return actionMsg{sid: sid, pull: r, lines: pullLines(p, r, at)}
		}
		if err := be.OpenInLive(p.WorkingPath()); err != nil {
			return actionMsg{sid: sid, lines: lines, err: err}
		}
		lines = append(lines, logLine{at, lInfo, "Opening " + p.SetName() + " in Live: " + p.WorkingPath()})
		return actionMsg{sid: sid, lines: lines}
	}
}

func (s *session) reloadCmd(sid int) tea.Cmd {
	p, now := s.p, s.now
	return func() tea.Msg {
		if err := p.Reload(); err != nil {
			return actionMsg{sid: sid, err: err}
		}
		return actionMsg{sid: sid, lines: []logLine{{now(), lWarn,
			p.SetName() + " now has everyone's changes - reopen it in Live (File > Open Recent), don't save the old one"}}}
	}
}

// catchupItems: zmiany kluczy pracy na zywo miedzy wersja wczytana w Live a
// head (przyszly w zapisanych wersjach innych). Liczone raz na pare wersji.
func catchupItems(p *store.Project, caught string) (string, []live.Catch) {
	head, loaded := p.Head(), p.Loaded()
	if head == "" || head == loaded {
		return "", nil
	}
	pair := loaded + ">" + head
	if pair == caught {
		return pair, nil
	}
	vs, err := p.Versions()
	if err != nil || vs[head] == nil || vs[loaded] == nil {
		return "", nil
	}
	a, err := p.LoadDoc(vs[loaded])
	if err != nil {
		return "", nil
	}
	b, err := p.LoadDoc(vs[head])
	if err != nil {
		return "", nil
	}
	return pair, live.CatchupItems(a, b)
}

// liveSyncable: zmiana, ktora wchodzi do otwartego Live'a bez przeladowania
// (na zywo przez skrypt albo z wersji - live.SetItems): nuty, petla i nazwa
// klipu MIDI, klip MIDI w slocie, panorama i wyciszenie sciezki, tempo.
// Kolor klipu, glosnosc i sendy - przez Reload (plik ma inne jednostki niz API).
func liveSyncable(c als.Change) bool {
	p := "/" + strings.Join(c.Path, "/") + "/"
	switch {
	case strings.Contains(p, "/MidiClip"):
		rest := p[strings.Index(p, "/MidiClip"):]
		rest = rest[strings.Index(rest[1:], "/")+1:]
		if rest == "/" { // caly klip dodany/usuniety (w slocie - skrypt to umie)
			return strings.Contains(p, "/ClipSlot")
		}
		for _, ok := range []string{"/Notes/", "/Loop/", "/Name/"} {
			if strings.HasPrefix(rest, ok) {
				return true
			}
		}
		return false
	case strings.Contains(p, "/Mixer/"):
		for _, ok := range []string{"/Pan/", "/Speaker/", "/Tempo/"} {
			if strings.Contains(p, ok) {
				return true
			}
		}
	}
	return false
}

// --- okno konfliktu na zywo ----------------------------------------------------------------

type liveConflictDialog struct {
	n   live.Note
	cur int
}

func (d *liveConflictDialog) form(a *App) form {
	w := min(110, a.w-6)
	half := (w - 9) / 2
	left := livePreview(d.n.Mine, half, 9)
	right := livePreview(d.n.Theirs, half, 9)
	body := []string{
		sWarnText.Render(trunc(d.n.Label, w-6)),
		sDim.Render("You and " + d.n.From + " changed this at the same time. Which version should stay?"), "",
		pad(sBold.Render("YOURS"), half) + "   " + sBold.Render(strings.ToUpper(d.n.From)+"'S"),
	}
	for i := 0; i < max(len(left), len(right)); i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		body = append(body, pad(l, half)+" │ "+r)
	}
	return form{title: "Live conflict with " + d.n.From, width: w, body: body, cur: d.cur,
		options: []option{{label: "Keep mine"}, {label: "Keep " + d.n.From + "'s"}},
		hint:    "enter choose · syncing of this clip is paused until you choose"}
}

func (d *liveConflictDialog) update(a *App, msg tea.Msg) (dialog, tea.Cmd) {
	i := optionKeys(&d.cur, 2, msg)
	if i < 0 || a.ses == nil || a.ses.live == nil {
		return d, nil
	}
	a.ses.live.Resolve(d.n.Key, i == 0)
	if len(a.ses.liveQueue) > 0 {
		next := a.ses.liveQueue[0]
		a.ses.liveQueue = a.ses.liveQueue[1:]
		return &liveConflictDialog{n: next}, nil
	}
	return nil, nil
}

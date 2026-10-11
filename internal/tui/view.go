package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/c-airr/ableton-collab/store"
	"github.com/c-airr/ableton-collab/transport/syncthing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// --- kolory i style ----------------------------------------------------------------------

var (
	cAccent = lipgloss.AdaptiveColor{Light: "#5A3FC0", Dark: "#A78BFA"}
	cCyan   = lipgloss.AdaptiveColor{Light: "#0E7490", Dark: "#22D3EE"}
	cDim    = lipgloss.AdaptiveColor{Light: "#6B6B6B", Dark: "#8A8A8A"}
	cLine   = lipgloss.AdaptiveColor{Light: "#9CA3AF", Dark: "#4B5563"}
	cOK     = lipgloss.AdaptiveColor{Light: "#1F7A3A", Dark: "#4ADE80"}
	cWarn   = lipgloss.AdaptiveColor{Light: "#9A6200", Dark: "#FBBF24"}
	cBad    = lipgloss.AdaptiveColor{Light: "#B42318", Dark: "#F87171"}
	cBtn    = lipgloss.AdaptiveColor{Light: "#E5E7EB", Dark: "#374151"}
	cBtnOn  = lipgloss.AdaptiveColor{Light: "#5A3FC0", Dark: "#7C3AED"}

	sTitle     = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sDim       = lipgloss.NewStyle().Foreground(cDim)
	sBold      = lipgloss.NewStyle().Bold(true)
	sKey       = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	sSel       = lipgloss.NewStyle().Reverse(true)
	sSelDim    = lipgloss.NewStyle().Underline(true)
	sDirName   = lipgloss.NewStyle().Foreground(cCyan)
	sSetName   = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	sAudioName = lipgloss.NewStyle()
	sDanger    = lipgloss.NewStyle().Foreground(cBad)
	sErr       = lipgloss.NewStyle().Foreground(cBad)
	sWarnText  = lipgloss.NewStyle().Foreground(cWarn)
	sOKText    = lipgloss.NewStyle().Foreground(cOK)
	sCode      = lipgloss.NewStyle().Bold(true).Foreground(cCyan)
	sInput     = lipgloss.NewStyle().Bold(true)
	sDialog    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(0, 1)
)

func levelStyle(l level) lipgloss.Style {
	switch l {
	case lOK:
		return sOKText
	case lWarn:
		return sWarnText
	case lErr:
		return sErr
	case lFile:
		return lipgloss.NewStyle().Foreground(cCyan)
	}
	return lipgloss.NewStyle()
}

// --- uklad ---------------------------------------------------------------------------------

// layout: wymiary i pozycje - wspolne dla rysowania i myszy.
//
//	FILES            | logo + status | PEOPLE
//	ACTIONS (przyciski)              | LOG / HISTORY
type layout struct {
	w, h                   int
	topH, botH             int
	leftW, centerW, rightW int
	btns                   []rect // przyciski w panelu ACTIONS (pozycje bezwzgledne)
}

type rect struct{ x, y, w, h int }

func (r rect) has(x, y int) bool { return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h }

const btnH = 3

func (a App) layout() layout {
	l := layout{w: max(60, a.w), h: max(20, a.h)}
	l.topH = max(12, l.h/2)
	l.botH = l.h - l.topH
	l.leftW = clamp(l.w*28/100, 22, 44)
	l.rightW = clamp(l.w*32/100, 26, 52)
	l.centerW = l.w - l.leftW - l.rightW
	// przyciski: bloki po 3 wiersze, zawijane do nastepnego rzedu
	inner := l.leftW + l.centerW - 4
	x, y := 0, 1
	for _, b := range a.buttons() {
		bw := buttonW(b)
		if x > 0 && x+bw > inner {
			x, y = 0, y+btnH+1
		}
		l.btns = append(l.btns, rect{x: 2 + x, y: l.topH + 1 + y, w: bw, h: btnH})
		x += bw + 2
	}
	return l
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }

func (a App) View() string {
	l := a.layout()
	top := lipgloss.JoinHorizontal(lipgloss.Top,
		box(a.filesTitle(), a.filesLines(l), l.leftW, l.topH, a.focus == fFiles && a.ses != nil),
		a.centerView(l),
		box(a.peopleTitle(), a.peopleLines(l), l.rightW, l.topH, a.focus == fPeople && a.ses != nil))
	logTitle, logLines := "LOG", a.logLines(l.rightW-4, l.botH-2)
	if a.center == cHistory {
		logTitle, logLines = "HISTORY", a.historyLines(l.rightW-4, l.botH-2)
	}
	bottom := lipgloss.JoinHorizontal(lipgloss.Top,
		box("ACTIONS", a.actionsLines(l), l.leftW+l.centerW, l.botH, a.focus == fButtons && a.dlg == nil),
		box(logTitle, logLines, l.rightW, l.botH, a.focus == fCenter && a.ses != nil))
	screen := top + "\n" + bottom
	if a.dlg != nil {
		screen = a.overlay(l, screen)
	}
	return screen
}

// box: panel z ramka i tytulem w ramce, dokladnie w x h.
func box(title string, lines []string, w, h int, focused bool) string {
	bc := lipgloss.NewStyle().Foreground(cLine)
	tc := lipgloss.NewStyle().Bold(true).Foreground(cDim)
	if focused {
		bc = lipgloss.NewStyle().Foreground(cAccent)
		tc = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	}
	inner := w - 4
	t := " " + ansi.Truncate(title, max(0, w-6), "…") + " "
	topLine := bc.Render("╭─") + tc.Render(t) + bc.Render(strings.Repeat("─", max(0, w-3-lipgloss.Width(t)))+"╮")
	out := []string{topLine}
	for i := 0; i < h-2; i++ {
		s := ""
		if i < len(lines) {
			s = ansi.Truncate(lines[i], inner, "…")
		}
		out = append(out, bc.Render("│")+" "+pad(s, inner)+" "+bc.Render("│"))
	}
	out = append(out, bc.Render("╰"+strings.Repeat("─", max(0, w-2))+"╯"))
	return strings.Join(out, "\n")
}

// overlay: okno na srodku ekranu, panel pod spodem przyciemniony.
func (a App) overlay(l layout, screen string) string {
	f := a.dlg.form(&a)
	b, _, _ := f.render(l.w)
	x0, y0 := dialogPos(l, b)
	bl := strings.Split(b, "\n")
	lines := strings.Split(screen, "\n")
	faint := lipgloss.NewStyle().Foreground(cLine)
	for i, ln := range lines {
		bg := faint.Render(ansi.Strip(ln))
		if j := i - y0; j >= 0 && j < len(bl) {
			bw := lipgloss.Width(bl[j])
			bg = ansi.Truncate(bg, x0, "") + bl[j] + ansi.TruncateLeft(bg, x0+bw, "")
		}
		lines[i] = bg
	}
	return strings.Join(lines, "\n")
}

func dialogPos(l layout, box string) (int, int) {
	return max(0, (l.w-lipgloss.Width(box))/2), max(0, (l.h-lipgloss.Height(box))/2)
}

// --- logo i status ---------------------------------------------------------------------------

var logoBig = []string{
	"▄▀█ █▄▄ █   █▀▀ ▀█▀ █▀█ █▄ █",
	"█▀█ █▄█ █▄▄ ██▄  █  █▄█ █ ▀█",
	"   █▀▀ █▀█ █   █   ▄▀█ █▄▄  ",
	"   █▄▄ █▄█ █▄▄ █▄▄ █▀█ █▄█  ",
}

func (a App) logo(w int) []string {
	if w < 32 {
		return []string{"ABLETON COLLAB"}
	}
	return logoBig
}

func (a App) centerView(l layout) string {
	center := func(s string) string {
		return lipgloss.PlaceHorizontal(l.centerW, lipgloss.Center, ansi.Truncate(s, l.centerW-2, "…"))
	}
	rows := []string{""}
	colors := []lipgloss.TerminalColor{cAccent, cAccent, cCyan, cCyan}
	for i, line := range a.logo(l.centerW) {
		rows = append(rows, center(lipgloss.NewStyle().Bold(true).Foreground(colors[i%len(colors)]).Render(line)))
	}
	rows = append(rows, "")
	for _, s := range a.statusLines(l.centerW - 4) {
		rows = append(rows, center(s))
	}
	for len(rows) < l.topH {
		rows = append(rows, "")
	}
	for i := range rows {
		rows[i] = pad(rows[i], l.centerW)
	}
	return strings.Join(rows[:l.topH], "\n")
}

// statusLines: stan pod logo - sesja, polaczenie i najwazniejsza sprawa teraz.
func (a App) statusLines(w int) []string {
	text, lv := a.status()
	icon := map[level]string{lInfo: "", lOK: "", lWarn: "⚠ ", lErr: "✗ ", lFile: "⇅ "}[lv]
	msg := []string{}
	for _, l := range wrapStyled(icon+text, w) {
		msg = append(msg, levelStyle(lv).Bold(lv != lInfo).Render(l))
	}
	if a.ses == nil {
		return append([]string{sDim.Render("no session"), ""}, msg...)
	}
	s := a.ses
	out := []string{sBold.Render(sessionName(s.p)) + sDim.Render("  ·  "+s.p.SetName()), a.connLine(), ""}
	return append(out, msg...)
}

// connLine: ilu ludzi jest polaczonych.
func (a App) connLine() string {
	s := a.ses
	switch {
	case s.net == nil:
		return sDim.Render("○ offline")
	case s.lobbyErr != nil:
		return sErr.Render("✗ network problem")
	case s.lobby == nil:
		return sDim.Render("○ connecting...")
	}
	on := 0
	for _, m := range s.lobby.Members {
		if m.Connected {
			on++
		}
	}
	switch {
	case on > 0:
		return sOKText.Render(fmt.Sprintf("● connected · %d of %d online", on, len(s.lobby.Members)))
	case len(s.lobby.Members) > 0:
		return sWarnText.Render(fmt.Sprintf("○ nobody online (%d in session)", len(s.lobby.Members)))
	}
	return sDim.Render("○ just you - Invite someone")
}

// logLines: ostatnie wpisy logu, dlugie zawijane (panel jest waski).
func (a App) logLines(w, h int) []string {
	if a.ses == nil {
		return []string{sDim.Render("Activity shows up here.")}
	}
	var all []string
	for _, ln := range a.ses.log {
		st := levelStyle(ln.level)
		for i, part := range wrapStyled(ln.text, w-6) {
			prefix := "      "
			if i == 0 {
				prefix = sDim.Render(ln.at.Format("15:04")) + " "
			}
			all = append(all, prefix+st.Render(part))
		}
	}
	end := max(0, len(all)-a.ses.logOff)
	return all[max(0, end-h):end]
}

// historyLines: wersje jak `git log --graph` (najnowsze na gorze).
func (a App) historyLines(w, h int) []string {
	if a.ses == nil {
		return nil
	}
	s := a.ses
	if len(s.versions) == 0 {
		return []string{sDim.Render("no versions yet")}
	}
	head := ""
	if s.st != nil && s.st.Head != nil {
		head = s.st.Head.ID
	}
	graph := versionGraph(s.versions)
	start := 0
	if s.histCur >= h {
		start = s.histCur - h + 1
	}
	var out []string
	for i := start; i < min(len(s.versions), start+h); i++ {
		v := s.versions[i]
		what := "saved"
		if len(v.Summary) > 0 {
			what = shortChange(v.Summary[0])
			if strings.HasPrefix(v.Summary[0], "merge") || strings.HasPrefix(v.Summary[0], "merged") || strings.HasPrefix(v.Summary[0], "first") {
				what = v.Summary[0]
			}
		}
		g := lipgloss.NewStyle().Foreground(cAccent).Render(graph[i])
		id := sDim.Render(v.Short()[:7])
		if v.ID == head {
			id = sOKText.Render(v.Short()[:7])
		}
		line := g + " " + id + " " + sBold.Render(v.Author) + " " + sDim.Render(when(v.Time)) + " " + what
		if i == s.histCur && a.focus == fCenter {
			line = sSel.Render(ansi.Strip(line))
		}
		out = append(out, line)
	}
	return out
}

// versionGraph: kolumny jak w gicie - kazda galaz ma swoj tor.
func versionGraph(vs []*store.Version) []string {
	var lanes []string
	out := make([]string, len(vs))
	for i, v := range vs {
		col := -1
		for j, id := range lanes {
			if id == v.ID {
				col = j
				break
			}
		}
		if col < 0 {
			lanes = append(lanes, v.ID)
			col = len(lanes) - 1
		}
		var g strings.Builder
		for j := range lanes {
			switch {
			case j == col && len(v.Parents) > 1:
				g.WriteString("◆")
			case j == col:
				g.WriteString("●")
			default:
				g.WriteString("│")
			}
			g.WriteString(" ")
		}
		out[i] = g.String()
		if len(v.Parents) == 0 {
			lanes = append(lanes[:col], lanes[col+1:]...)
			continue
		}
		lanes[col] = v.Parents[0]
		for _, p := range v.Parents[1:] {
			found := false
			for _, id := range lanes {
				found = found || id == p
			}
			if !found {
				lanes = append(lanes, p)
			}
		}
		// ten sam rodzic w dwoch torach - galezie sie schodza
		for j := range lanes {
			if j != col && lanes[j] == lanes[col] {
				lanes = append(lanes[:col], lanes[col+1:]...)
				break
			}
		}
	}
	w := 0
	for _, g := range out {
		w = max(w, lipgloss.Width(g))
	}
	for i := range out {
		out[i] = pad(out[i], w)
	}
	return out
}

func when(t time.Time) string {
	if time.Since(t) < 20*time.Hour {
		return t.Local().Format("15:04")
	}
	return t.Local().Format("01-02 15:04")
}

// --- pliki -----------------------------------------------------------------------------------

func (a App) filesTitle() string {
	if a.ses == nil {
		return "FILES"
	}
	return "FILES · " + sessionName(a.ses.p)
}

func (a App) filesLines(l layout) []string {
	if a.ses == nil {
		return []string{sDim.Render("No session yet.")}
	}
	return a.ses.tree.lines(l.leftW-4, l.topH-2, a.focus == fFiles, a.ses.p.Meta.Set)
}

// --- ludzie ----------------------------------------------------------------------------------

func (a App) peopleTitle() string {
	if a.ses == nil || a.ses.lobby == nil {
		return "PEOPLE"
	}
	return fmt.Sprintf("PEOPLE %d/%d", len(a.ses.lobby.Members)+1, syncthing.MaxMembers)
}

// peopleRows: wiersze panelu i indeks zaznaczenia dla kazdego wiersza (-1: nie do wyboru).
func (a App) peopleRows(w int) ([]string, []int) {
	if a.ses == nil {
		return []string{sDim.Render("Join or host a session"), sDim.Render("to see who's here.")}, []int{-1, -1}
	}
	s := a.ses
	me := sOKText.Render("● ") + sBold.Render(trunc(a.me.Name, w-12)) + sDim.Render("  you")
	if s.isHost() {
		me += sDim.Render(", host")
	}
	lines, idx := []string{me}, []int{-1}
	if s.net == nil {
		return append(lines, "", sDim.Render("Offline - no network.")), append(idx, -1, -1)
	}
	if s.lobbyErr != nil {
		return append(lines, "", sErr.Render("Network: "+s.lobbyErr.Error())), append(idx, -1, -1)
	}
	members, reqs := s.peopleRows()
	n := 0
	for _, m := range members {
		dot, how := sDim.Render("○ "), sDim.Render("offline")
		if m.Connected {
			dot, how = sOKText.Render("● "), sDim.Render(viaLabel(m.Via))
		}
		rel := ""
		if ps := s.peerStatus(m.ID); ps != nil {
			rel = relIcon(ps.Relation)
		}
		line := dot + pad(trunc(m.Name, w-16), w-14) + rel + " " + how
		if n == s.peerCur && a.focus == fPeople {
			line = sSel.Render(pad(ansi.Strip(line), w))
		}
		lines, idx = append(lines, line), append(idx, n)
		n++
	}
	if len(members) == 0 && len(reqs) == 0 {
		lines = append(lines, "", sDim.Render("Nobody else yet - Invite someone."))
		idx = append(idx, -1, -1)
	}
	if len(reqs) > 0 {
		lines, idx = append(lines, "", sWarnText.Render("Wants to join:")), append(idx, -1, -1)
		for _, r := range reqs {
			line := sWarnText.Render("? ") + pad(trunc(r.Name, w-14), w-12) + sDim.Render(syncthing.ShortID(r.ID))
			if n == s.peerCur && a.focus == fPeople {
				line = sSel.Render(pad(ansi.Strip(line), w))
			}
			lines, idx = append(lines, line), append(idx, n)
			n++
		}
	}
	return lines, idx
}

func (a App) peopleLines(l layout) []string {
	lines, _ := a.peopleRows(l.rightW - 4)
	return lines
}

func viaLabel(v string) string {
	switch v {
	case "LAN":
		return "LAN"
	case "internet":
		return "direct"
	case "relay":
		return "relay"
	}
	return v
}

func relIcon(r store.Relation) string {
	switch r {
	case store.RelSame:
		return sOKText.Render("✓")
	case store.RelAhead:
		return sWarnText.Render("⇣")
	case store.RelBehind:
		return sWarnText.Render("⇡")
	case store.RelDiverged:
		return sErr.Render("⚡")
	case store.RelIncomplete:
		return sDim.Render("…")
	}
	return sDim.Render("·")
}

// --- przyciski (panel ACTIONS) ----------------------------------------------------------------

func buttonW(b button) int { return max(12, lipgloss.Width(b.label)+lipgloss.Width(b.key)+6) }

// buttonBlock: przycisk jako pelny blok 3 wiersze (jak w wifite).
func buttonBlock(b button, w int, on bool) []string {
	bg := lipgloss.NewStyle().Background(cBtn)
	fg := bg.Foreground(lipgloss.AdaptiveColor{Light: "#111827", Dark: "#F3F4F6"})
	key := bg.Foreground(cDim)
	if b.danger {
		fg = fg.Foreground(cBad)
	}
	if on {
		bg = lipgloss.NewStyle().Background(cBtnOn)
		fg = bg.Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
		key = bg.Foreground(lipgloss.Color("#DDD6FE"))
	}
	gap := w - 4 - lipgloss.Width(b.label) - lipgloss.Width(b.key)
	mid := bg.Render("  ") + fg.Render(b.label) + bg.Render(strings.Repeat(" ", max(1, gap))) + key.Render(b.key) + bg.Render("  ")
	blank := bg.Render(strings.Repeat(" ", w))
	return []string{blank, mid, blank}
}

// actionsLines: zawartosc panelu ACTIONS - przyciski (wg pozycji z layout),
// pod nimi co jest zaznaczone i podpowiedz klawiszy.
func (a App) actionsLines(l layout) []string {
	inner, h := l.leftW+l.centerW-4, l.botH-2
	lines := make([]string, h)
	bs := a.buttons()
	for i, r := range l.btns {
		block := buttonBlock(bs[i], r.w, a.focus == fButtons && i == a.btn && a.dlg == nil)
		for j, part := range block {
			y := r.y - l.topH - 1 + j
			if y < 0 || y >= h {
				continue
			}
			x := r.x - 2
			lines[y] = pad(lines[y], x) + part
		}
	}
	if sel := a.selection(); sel != "" && h >= 2 {
		lines[h-2] = sDim.Render("selected: ") + sel
	}
	if h >= 1 {
		lines[h-1] = sDim.Render(ansi.Truncate("tab: next panel · ←→ pick · enter: press · mouse works too", inner, "…"))
	}
	return lines
}

// selection: co jest zaznaczone w aktywnym panelu (dla przyciskow kontekstowych).
func (a App) selection() string {
	s := a.ses
	if s == nil {
		return ""
	}
	switch a.focus {
	case fFiles:
		for _, n := range s.tree.all {
			if n.rel == s.tree.sel {
				info := humanBytes(n.size)
				if n.dir {
					info = fmt.Sprintf("%d files, %s", n.count, humanBytes(n.size))
				}
				return n.rel + sDim.Render("  ·  "+info)
			}
		}
	case fPeople:
		if m := s.selMember(); m != nil {
			how := "offline"
			if m.Connected {
				how = viaLabel(m.Via)
			}
			return m.Name + sDim.Render("  ·  "+how+"  ·  "+syncthing.ShortID(m.ID))
		}
		if r := s.selRequest(); r != nil {
			return r.Name + sDim.Render("  ·  wants to join  ·  code "+syncthing.ShortID(r.ID))
		}
	}
	return ""
}

// status: najwazniejsza rzecz do powiedzenia teraz.
func (a App) status() (string, level) {
	if a.note.text != "" && a.now().Before(a.note.until) {
		return a.note.text, a.note.level
	}
	s := a.ses
	if s == nil {
		return "Join a friend's session with their code, or Host your own.", lInfo
	}
	switch {
	case s.busy != "" && s.busy != refreshing:
		return "⟳ " + strings.ToUpper(s.busy[:1]) + s.busy[1:] + "...", lWarn
	case s.err != nil:
		return s.err.Error(), lErr
	case s.lobbyErr != nil:
		return "Network problem: " + s.lobbyErr.Error(), lErr
	}
	if s.lobby != nil && len(s.lobby.Requests) > 0 {
		r := s.lobby.Requests[0]
		return fmt.Sprintf("%s wants to join - select them in PEOPLE, then Accept", r.Name), lWarn
	}
	if s.st != nil {
		for _, ps := range s.st.Peers {
			if ps.Relation == store.RelDiverged {
				return fmt.Sprintf("You and %s both made changes - Get changes to merge them", ps.Device.Name), lWarn
			}
		}
		for _, ps := range s.st.Peers {
			if ps.Relation == store.RelAhead {
				return fmt.Sprintf("%s has new changes - press Get changes", ps.Device.Name), lWarn
			}
		}
		if s.st.Head == nil {
			return "You don't have the set yet - press Get changes", lWarn
		}
		if n := len(s.st.Changes); n > 0 {
			return fmt.Sprintf("Your set has %d unsaved change(s) - press Save to share them", n), lInfo
		}
	}
	if s.lobby != nil && s.lobby.Sync != nil && s.lobby.Sync.NeedFiles > 0 {
		return fmt.Sprintf("Receiving %d file(s), %s...", s.lobby.Sync.NeedFiles, humanBytes(s.lobby.Sync.NeedBytes)), lFile
	}
	return "✓ Everything is up to date", lOK
}

// --- pomocnicze ------------------------------------------------------------------------------

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

// pad: dopelnienie do szerokosci widocznej (kody kolorow nie zajmuja miejsca).
func pad(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

func wrap(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

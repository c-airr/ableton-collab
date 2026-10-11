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
type layout struct {
	w, h                   int
	topH                   int
	leftW, centerW, rightW int
	logoH                  int // wiersze logo + podpis nad panelem logu
	btnY, statusY          int
	btns                   [][2]int // zakres x przyciskow
}

func (a App) layout() layout {
	l := layout{w: max(60, a.w), h: max(18, a.h)}
	l.statusY = l.h - 1
	l.btnY = l.h - 2
	l.topH = l.h - 2
	l.leftW = clamp(l.w*30/100, 22, 46)
	l.rightW = clamp(l.w*27/100, 22, 40)
	l.centerW = l.w - l.leftW - l.rightW
	l.logoH = len(a.logo(l.centerW)) + 2
	x := 1
	for _, b := range a.buttons() {
		bw := lipgloss.Width(buttonText(b))
		l.btns = append(l.btns, [2]int{x, x + bw})
		x += bw + 1
	}
	return l
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }

func (a App) View() string {
	l := a.layout()
	var top string
	if a.dlg != nil {
		top = a.dialogArea(l)
	} else {
		left := box(a.filesTitle(), a.filesLines(l), l.leftW, l.topH, a.focus == fFiles && a.ses != nil)
		mid := a.centerView(l)
		right := box(a.peopleTitle(), a.peopleLines(l), l.rightW, l.topH, a.focus == fPeople && a.ses != nil)
		top = lipgloss.JoinHorizontal(lipgloss.Top, left, mid, right)
	}
	return top + "\n" + a.buttonsLine(l) + "\n" + a.statusLine(l)
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

// dialogArea: okno na srodku obszaru paneli.
func (a App) dialogArea(l layout) string {
	f := a.dlg.form(&a)
	b, _, _ := f.render(l.w)
	x0, y0 := dialogPos(l, b)
	lines := strings.Split(b, "\n")
	out := make([]string, l.topH)
	for i := range out {
		if j := i - y0; j >= 0 && j < len(lines) {
			out[i] = pad(strings.Repeat(" ", x0)+lines[j], l.w)
		} else {
			out[i] = strings.Repeat(" ", l.w)
		}
	}
	return strings.Join(out, "\n")
}

func dialogPos(l layout, box string) (int, int) {
	return max(0, (l.w-lipgloss.Width(box))/2), max(0, (l.topH-lipgloss.Height(box))/2)
}

// --- logo i srodek ---------------------------------------------------------------------------

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
	var rows []string
	colors := []lipgloss.TerminalColor{cAccent, cAccent, cCyan, cCyan}
	for i, line := range a.logo(l.centerW) {
		st := lipgloss.NewStyle().Bold(true).Foreground(colors[i%len(colors)])
		rows = append(rows, lipgloss.PlaceHorizontal(l.centerW, lipgloss.Center, st.Render(line)))
	}
	sub := sDim.Render("no session")
	if a.ses != nil {
		sub = sBold.Render(sessionName(a.ses.p)) + sDim.Render("  ·  "+a.ses.p.SetName()+"  ·  "+a.me.Name)
	}
	rows = append(rows, lipgloss.PlaceHorizontal(l.centerW, lipgloss.Center, ansi.Truncate(sub, l.centerW-2, "…")), "")
	h := l.topH - len(rows)
	title, lines := "LOG", a.logLines(l.centerW-4, h-2)
	if a.center == cHistory {
		title, lines = "HISTORY", a.historyLines(l.centerW-4, h-2)
	}
	rows = append(rows, box(title, lines, l.centerW, h, a.focus == fCenter && a.ses != nil))
	return strings.Join(rows, "\n")
}

func (a App) logLines(w, h int) []string {
	if a.ses == nil {
		return []string{
			sDim.Render("Join a session with a code from a friend,"),
			sDim.Render("or Host your own - pick a folder and invite people."),
		}
	}
	s := a.ses
	end := max(0, len(s.log)-s.logOff)
	start := max(0, end-h)
	var out []string
	for _, ln := range s.log[start:end] {
		out = append(out, sDim.Render(ln.at.Format("15:04"))+" "+levelStyle(ln.level).Render(ln.text))
	}
	return out
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

// --- przyciski i pasek ---------------------------------------------------------------------

func buttonText(b button) string { return " " + b.label + " " + b.key + " " }

func (a App) buttonsLine(l layout) string {
	var parts []string
	for i, b := range a.buttons() {
		if a.focus == fButtons && i == a.btn {
			st := lipgloss.NewStyle().Background(cBtnOn).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
			parts = append(parts, st.Render(buttonText(b)))
			continue
		}
		st := lipgloss.NewStyle().Background(cBtn)
		if b.danger {
			st = st.Foreground(cBad)
		}
		parts = append(parts, st.Render(" "+b.label+" ")+lipgloss.NewStyle().Background(cBtn).Foreground(cDim).Render(b.key+" "))
	}
	return " " + strings.Join(parts, " ")
}

// status: najwazniejsza rzecz do powiedzenia teraz.
func (a App) status() (string, level) {
	if a.note.text != "" && a.now().Before(a.note.until) {
		return a.note.text, a.note.level
	}
	s := a.ses
	if s == nil {
		return "No session - Join one with a code from a friend, or Host your own.", lInfo
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
		return fmt.Sprintf("%s wants to join - select them in PEOPLE and Accept", r.Name), lWarn
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

func (a App) statusLine(l layout) string {
	text, lv := a.status()
	icon := map[level]string{lInfo: "ℹ ", lOK: "", lWarn: "⚠ ", lErr: "✗ ", lFile: "⇅ "}[lv]
	right := "tab: switch panel  "
	room := l.w - lipgloss.Width(right) - 2
	msg := " " + ansi.Truncate(icon+text, room, "…")
	return levelStyle(lv).Bold(lv == lErr || lv == lWarn).Render(pad(msg, l.w-lipgloss.Width(right))) + sDim.Render(right)
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

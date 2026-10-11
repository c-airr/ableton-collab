package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// dirFit: sciezka skrocona od lewej - koniec (nazwa folderu) zawsze widoczny.
func dirFit(p string, n int) string {
	r := []rune(p)
	if len(r) <= n || n < 4 {
		return p
	}
	return "…" + string(r[len(r)-n+1:])
}

// dialog: okno nad panelem. update dostaje klawisze, klikniecia (chooseMsg)
// i komunikaty asynchroniczne; zwraca nastepne okno (nil - zamkniete).
type dialog interface {
	update(a *App, msg tea.Msg) (dialog, tea.Cmd)
	form(a *App) form
}

// chooseMsg: klikniecie myszka w opcje i.
type chooseMsg struct{ i int }

// form: to, co okno pokazuje. Ten sam opis sluzy do rysowania i do mapowania
// klikniec, wiec pozycje zawsze sie zgadzaja.
type form struct {
	title   string
	body    []string
	options []option
	cur     int
	input   *textInput
	path    *pathInput
	foot    []string // dodatkowe linie pod opcjami (np. podglad)
	hint    string
	err     string
	width   int // 0 - domyslna
}

type option struct {
	label  string
	detail string
	danger bool
}

// --- pole tekstowe -----------------------------------------------------------------------

type textInput struct {
	value string
	mask  bool
}

// key: edycja; zwraca true, jesli klawisz byl dla pola.
func (t *textInput) key(k tea.KeyMsg) bool {
	switch k.Type {
	case tea.KeyBackspace:
		if r := []rune(t.value); len(r) > 0 {
			t.value = string(r[:len(r)-1])
		}
	case tea.KeyCtrlU:
		t.value = ""
	case tea.KeySpace:
		t.value += " "
	case tea.KeyRunes:
		t.value += string(k.Runes)
	default:
		return false
	}
	return true
}

// --- pole folderu (jak /cd w Claude Code) -------------------------------------------------

// pathInput: wpisujesz sciezke, pod spodem podpowiedzi pasujacych folderow.
// Tab (albo Enter na podswietlonej) wchodzi w podpowiedz i pokazuje jej
// podfoldery; Enter bez podswietlenia zatwierdza wpisany folder.
type pathInput struct {
	textInput
	sugg []string // pelne sciezki
	cur  int      // -1: nic nie podswietlone
}

const sep = string(os.PathSeparator)

func newPathInput(start string) *pathInput {
	p := &pathInput{cur: -1}
	if start != "" {
		p.value = strings.TrimRight(start, `/\`) + sep
	}
	p.refresh()
	return p
}

func expandHome(v string) string {
	if v == "~" || strings.HasPrefix(v, "~/") || strings.HasPrefix(v, `~\`) {
		if h, err := os.UserHomeDir(); err == nil {
			return h + v[1:]
		}
	}
	return v
}

// Path: wpisana sciezka (bezwzgledna, bez koncowego separatora).
func (p *pathInput) Path() string {
	v := cleanPath(expandHome(p.value))
	if v == "" {
		return ""
	}
	if runtime.GOOS == "windows" && len(v) == 2 && v[1] == ':' {
		v += sep
	}
	if abs, err := filepath.Abs(v); err == nil {
		return abs
	}
	return v
}

func (p *pathInput) refresh() {
	p.sugg, p.cur = nil, -1
	v := expandHome(p.value)
	if v == "" {
		if runtime.GOOS == "windows" {
			p.sugg = drives()
		}
		return
	}
	dir, prefix := v, ""
	if !strings.HasSuffix(v, "/") && !strings.HasSuffix(v, `\`) {
		dir, prefix = filepath.Dir(v), filepath.Base(v)
		if runtime.GOOS == "windows" && len(v) == 2 && v[1] == ':' {
			dir, prefix = v+sep, ""
		}
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() || strings.HasPrefix(n, ".") || strings.HasPrefix(n, "$") {
			continue
		}
		if strings.HasPrefix(strings.ToLower(n), strings.ToLower(prefix)) {
			p.sugg = append(p.sugg, filepath.Join(dir, n))
		}
	}
	sort.Slice(p.sugg, func(i, j int) bool { return strings.ToLower(p.sugg[i]) < strings.ToLower(p.sugg[j]) })
}

// complete: wejdz w podpowiedz i (pokaze jej podfoldery).
func (p *pathInput) complete(i int) {
	if i < 0 || i >= len(p.sugg) {
		return
	}
	p.value = strings.TrimRight(p.sugg[i], `/\`) + sep
	p.refresh()
}

// key: zwraca submit=true, gdy Enter zatwierdza wpisany folder.
func (p *pathInput) key(k tea.KeyMsg) (submit, handled bool) {
	switch k.String() {
	case "up":
		if p.cur > -1 {
			p.cur--
		}
		return false, true
	case "down":
		if p.cur < len(p.sugg)-1 {
			p.cur++
		}
		return false, true
	case "tab":
		i := p.cur
		if i < 0 {
			i = 0
		}
		p.complete(i)
		return false, true
	case "enter":
		if p.cur >= 0 {
			p.complete(p.cur)
			return false, true
		}
		// "I:/co" -> jedyna podpowiedz I:/code, a I:/co nie istnieje: wchodzimy
		if _, err := os.Stat(p.Path()); err != nil && len(p.sugg) == 1 {
			p.complete(0)
			return false, true
		}
		return true, true
	}
	if p.textInput.key(k) {
		p.refresh()
		return false, true
	}
	return false, false
}

// ghost: dopelnienie pierwszej podpowiedzi (szare, za kursorem).
func (p *pathInput) ghost() string {
	if p.cur >= 0 || len(p.sugg) == 0 || strings.HasSuffix(p.value, sep) || strings.HasSuffix(p.value, "/") {
		return ""
	}
	base := filepath.Base(expandHome(p.value))
	name := filepath.Base(p.sugg[0])
	if len(name) > len(base) && strings.EqualFold(name[:len(base)], base) {
		return name[len(base):]
	}
	return ""
}

// --- rysowanie okna ----------------------------------------------------------------------

const suggRows = 7

func (f form) boxWidth(maxW int) int {
	w := f.width
	if w == 0 {
		w = 72
	}
	return max(30, min(w, maxW-4))
}

// render: okno i pozycje opcji/podpowiedzi (wiersze wzgledem gory okna).
func (f form) render(maxW int) (string, map[int]int, map[int]int) {
	w := f.boxWidth(maxW)
	inner := w - 4
	var lines []string
	optRow, suggRow := map[int]int{}, map[int]int{}
	lines = append(lines, sTitle.Render(f.title), "")
	for _, b := range f.body {
		for _, l := range wrapStyled(b, inner) {
			lines = append(lines, ansi.Truncate(l, inner, "…"))
		}
	}
	if len(f.body) > 0 {
		lines = append(lines, "")
	}
	if f.input != nil {
		v := f.input.value
		lines = append(lines, inputLine(v, "", inner))
		lines = append(lines, "")
	}
	if f.path != nil {
		lines = append(lines, inputLine(f.path.value, f.path.ghost(), inner))
		start := 0
		if f.path.cur >= suggRows {
			start = f.path.cur - suggRows + 1
		}
		for i := start; i < min(len(f.path.sugg), start+suggRows); i++ {
			name := filepath.Base(f.path.sugg[i]) + sep
			if len(f.path.value) == 0 {
				name = f.path.sugg[i]
			}
			row := "  " + trunc(name, inner-4)
			if i == f.path.cur {
				row = sSel.Render(pad(row, inner))
			} else {
				row = sDim.Render(row)
			}
			suggRow[len(lines)] = i
			lines = append(lines, row)
		}
		if len(f.path.sugg) > suggRows {
			lines = append(lines, sDim.Render("  …"))
		}
		lines = append(lines, "")
	}
	for i, o := range f.options {
		label := o.label
		mark := "  "
		st := lipgloss.NewStyle()
		if o.danger {
			st = sDanger
		}
		if i == f.cur {
			mark = sKey.Render("› ")
			st = st.Bold(true)
		}
		optRow[len(lines)] = i
		lines = append(lines, mark+st.Render(trunc(label, inner-2)))
		if o.detail != "" {
			for _, d := range wrapStyled(o.detail, inner-4) {
				optRow[len(lines)] = i
				lines = append(lines, "    "+sDim.Render(d))
			}
		}
	}
	if len(f.options) > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, f.foot...)
	if f.err != "" {
		for _, e := range wrapStyled(f.err, inner) {
			lines = append(lines, sErr.Render(e))
		}
		lines = append(lines, "")
	}
	if f.hint != "" {
		lines = append(lines, sDim.Render(trunc(f.hint, inner)))
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	box := sDialog.Width(w - 2).Render(strings.Join(lines, "\n"))
	// +1: gorna ramka
	shift := func(m map[int]int) map[int]int {
		out := map[int]int{}
		for k, v := range m {
			out[k+1] = v
		}
		return out
	}
	return box, shift(optRow), shift(suggRow)
}

func inputLine(v, ghost string, w int) string {
	r := []rune(v)
	if len(r) > w-3 {
		v = "…" + string(r[len(r)-(w-4):])
	}
	return sInput.Render("› "+v) + sDim.Render(ghost) + sKey.Render("▏")
}

// wrapStyled: lamanie zwyklego tekstu na slowa (linie juz ostylowane - bez lamania).
func wrapStyled(s string, w int) []string {
	if lipgloss.Width(s) <= w || strings.Contains(s, "\x1b") {
		return []string{s}
	}
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		if line != "" && lipgloss.Width(line)+1+lipgloss.Width(word) > w {
			out = append(out, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	return append(out, line)
}

// optionKeys: wspolna obsluga listy opcji (strzalki, enter, klikniecie).
// Zwraca wybrana opcje albo -1.
func optionKeys(f *int, n int, msg tea.Msg) int {
	switch m := msg.(type) {
	case chooseMsg:
		*f = m.i
		return m.i
	case tea.KeyMsg:
		switch m.String() {
		case "up", "k":
			if *f > 0 {
				*f--
			}
		case "down", "j", "tab":
			if *f < n-1 {
				*f++
			}
		case "enter":
			return *f
		default:
			if len(m.Runes) == 1 && m.Runes[0] >= '1' && m.Runes[0] <= '9' {
				if i := int(m.Runes[0] - '1'); i < n {
					*f = i
					return i
				}
			}
		}
	}
	return -1
}

func isEsc(msg tea.Msg) bool {
	k, ok := msg.(tea.KeyMsg)
	return ok && k.String() == "esc"
}

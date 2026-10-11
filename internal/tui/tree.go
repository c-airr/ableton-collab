package tui

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/c-airr/ableton-collab/store"

	"github.com/charmbracelet/lipgloss"
)

// treeNode: plik albo folder sesji (rel: sciezka wzgledem folderu sesji, "/").
type treeNode struct {
	rel   string
	name  string
	dir   bool
	size  int64 // pliki: rozmiar; foldery: suma
	count int   // foldery: liczba plikow w srodku
	depth int
}

// tree: drzewo plikow jak w eksploratorze - foldery zwijane/rozwijane.
type tree struct {
	root  string
	all   []treeNode
	open  map[string]bool
	sel   string // zaznaczony wezel (rel) - przezywa odswiezenie listy
	off   int
	ready bool
}

func newTree(root string) tree { return tree{root: root, open: map[string]bool{}} }

// hidden: pliki abcollab/Syncthinga i tymczasowe - nie pokazujemy.
func hidden(name string) bool {
	return name == store.Dir || strings.HasPrefix(name, ".st") || strings.HasPrefix(name, "~syncthing~") ||
		strings.HasSuffix(name, ".tmp") || name == "desktop.ini" || name == ".DS_Store"
}

// scanTree: cale drzewo folderu sesji (foldery najpierw, alfabetycznie), z limitem.
func scanTree(root string, limit int) []treeNode {
	var out []treeNode
	var walk func(dir, rel string, depth int) (int64, int)
	walk = func(dir, rel string, depth int) (int64, int) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return 0, 0
		}
		sort.Slice(ents, func(i, j int) bool {
			if ents[i].IsDir() != ents[j].IsDir() {
				return ents[i].IsDir()
			}
			return strings.ToLower(ents[i].Name()) < strings.ToLower(ents[j].Name())
		})
		var size int64
		count := 0
		for _, e := range ents {
			if hidden(e.Name()) || len(out) >= limit {
				continue
			}
			r := path.Join(rel, e.Name())
			n := treeNode{rel: r, name: e.Name(), dir: e.IsDir(), depth: depth}
			idx := len(out)
			out = append(out, n)
			if e.IsDir() {
				s, c := walk(filepath.Join(dir, e.Name()), r, depth+1)
				out[idx].size, out[idx].count = s, c
				size += s
				count += c
			} else if fi, err := e.Info(); err == nil {
				out[idx].size = fi.Size()
				size += fi.Size()
				count++
			}
		}
		return size, count
	}
	walk(root, "", 0)
	return out
}

// set: nowa zawartosc po odswiezeniu; przy pierwszym razie rozwijamy foldery
// prowadzace do seta sesji.
func (t *tree) set(nodes []treeNode) {
	t.all = nodes
	if !t.ready && len(nodes) > 0 {
		t.ready = true
		if t.sel == "" {
			t.sel = nodes[0].rel
		}
	}
}

// expandTo rozwija foldery nad plikiem rel.
func (t *tree) expandTo(rel string) {
	for d := path.Dir(rel); d != "." && d != "/" && d != ""; d = path.Dir(d) {
		t.open[d] = true
	}
}

func (t *tree) visible() []treeNode {
	var out []treeNode
	for _, n := range t.all {
		ok := true
		for d := path.Dir(n.rel); d != "." && d != ""; d = path.Dir(d) {
			if !t.open[d] {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, n)
		}
	}
	return out
}

func (t *tree) cursor(vis []treeNode) int {
	for i, n := range vis {
		if n.rel == t.sel {
			return i
		}
	}
	return 0
}

func (t *tree) key(k string) bool {
	vis := t.visible()
	if len(vis) == 0 {
		return false
	}
	i := t.cursor(vis)
	n := vis[i]
	switch k {
	case "up":
		if i > 0 {
			t.sel = vis[i-1].rel
		}
	case "down":
		if i < len(vis)-1 {
			t.sel = vis[i+1].rel
		}
	case "enter", " ":
		if n.dir {
			t.open[n.rel] = !t.open[n.rel]
		}
	case "right":
		if n.dir {
			t.open[n.rel] = true
		}
	case "left":
		if n.dir && t.open[n.rel] {
			t.open[n.rel] = false
		} else if p := path.Dir(n.rel); p != "." {
			t.sel = p
		}
	case "home":
		t.sel = vis[0].rel
	case "end":
		t.sel = vis[len(vis)-1].rel
	default:
		return false
	}
	return true
}

// click: klikniecie w wiersz i (od gory widocznej listy).
func (t *tree) click(row int) {
	vis := t.visible()
	if i := t.off + row; i >= 0 && i < len(vis) {
		if t.sel == vis[i].rel && vis[i].dir {
			t.open[vis[i].rel] = !t.open[vis[i].rel]
		}
		t.sel = vis[i].rel
	}
}

// selectedDeletable: sciezka zaznaczonego wezla, jesli wolno go usunac -
// nie set sesji i nie folder, w ktorym on lezy.
func (t *tree) selectedDeletable(p *store.Project) string {
	if t.sel == "" || !t.ready {
		return ""
	}
	set := p.Meta.Set
	if t.sel == set || strings.HasPrefix(set, t.sel+"/") {
		return ""
	}
	for _, n := range t.all {
		if n.rel == t.sel {
			return filepath.Join(p.Dir, filepath.FromSlash(n.rel))
		}
	}
	return ""
}

// lines: wiersze panelu (szerokosc w, wysokosc h).
func (t *tree) lines(w, h int, focused bool, set string) []string {
	vis := t.visible()
	if len(vis) == 0 {
		return []string{sDim.Render("(empty folder)")}
	}
	i := t.cursor(vis)
	if i < t.off {
		t.off = i
	}
	if i >= t.off+h {
		t.off = i - h + 1
	}
	t.off = max(0, min(t.off, len(vis)-h))
	var out []string
	for j := t.off; j < min(len(vis), t.off+h); j++ {
		n := vis[j]
		icon := "  "
		if n.dir {
			icon = "▸ "
			if t.open[n.rel] {
				icon = "▾ "
			}
		}
		name := n.name
		style := lipgloss.NewStyle()
		switch {
		case n.dir:
			style = sDirName
		case n.rel == set:
			style = sSetName
			name += " ★"
		case isAudio(n.name):
			style = sAudioName
		}
		right := humanBytes(n.size)
		if n.dir {
			right = fmt.Sprintf("%d", n.count)
		}
		left := strings.Repeat("  ", n.depth) + icon
		room := w - lipgloss.Width(left) - len(right) - 1
		var line string
		line = pad(left+style.Render(trunc(name, room)), w-len(right)) + sDim.Render(right)
		if j == i && focused {
			line = sSel.Render(pad(left+trunc(name, room), w-len(right)) + right)
		} else if j == i {
			line = sSelDim.Render(pad(left+trunc(name, room), w-len(right)) + right)
		}
		out = append(out, line)
	}
	return out
}

func isAudio(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".wav", ".mp3", ".aif", ".aiff", ".flac", ".ogg", ".m4a":
		return true
	}
	return false
}

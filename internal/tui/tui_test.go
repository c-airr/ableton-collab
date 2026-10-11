package tui

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/c-airr/ableton-collab/als"
	"github.com/c-airr/ableton-collab/store"

	tea "github.com/charmbracelet/bubbletea"
)

func testdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Skip("brak testdata/" + name)
	}
	return data
}

// run wykonuje komende i kolejne komendy z odpowiedzi (bez tickow).
func run(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	for i := 0; cmd != nil && i < 10; i++ {
		msg := cmd()
		if _, ok := msg.(tickMsg); ok {
			break
		}
		var next tea.Model
		next, cmd = m.Update(msg)
		m = next.(Model)
	}
	return m
}

func key(t *testing.T, m Model, k string) Model {
	t.Helper()
	var msg tea.KeyMsg
	switch k {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
	next, cmd := m.Update(msg)
	return run(t, next.(Model), cmd)
}

func project(t *testing.T, name string, set []byte) *store.Project {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "song.als"), set, 0o644)
	p, _, err := store.Init(dir, "song.als", store.NewDevice(name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		rel, _ := filepath.Rel(src, path)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, _ := os.ReadFile(path)
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
}

func shareDevice(t *testing.T, from, to *store.Project) {
	rel := filepath.Join(store.Dir, "devices", from.Me.ID)
	copyDir(t, filepath.Join(from.Dir, rel), filepath.Join(to.Dir, rel))
}

func TestStatusCommitAndHistory(t *testing.T) {
	p := project(t, "Ja", testdata(t, "base.als"))
	m := New(p)
	m = run(t, m, m.refreshCmd(true))
	v := m.View()
	if !strings.Contains(v, "Twoja wersja") || !strings.Contains(v, "set zgodny z wersja") {
		t.Fatalf("widok statusu:\n%s", v)
	}
	os.WriteFile(p.WorkingPath(), testdata(t, "ours.als"), 0o644)
	m = key(t, m, "r")
	if v := m.View(); !strings.Contains(v, "1 zmian(y)") || !strings.Contains(v, "8-12 String Guitar") {
		t.Fatalf("brak niezapisanych zmian:\n%s", v)
	}
	m = key(t, m, "c")
	if v := m.View(); !strings.Contains(v, "Zapisano wersje") || !strings.Contains(v, "set zgodny z wersja") {
		t.Fatalf("po commicie:\n%s", v)
	}
	m = key(t, m, "2")
	if v := m.View(); strings.Count(v, "Ja") < 2 || !strings.Contains(v, "pierwsza wersja") && !strings.Contains(v, "8-12 String Guitar") {
		t.Fatalf("historia:\n%s", v)
	}
	m = key(t, m, "3")
	if v := m.View(); !strings.Contains(v, "song.als") || !strings.Contains(v, "Co musi miec") {
		t.Fatalf("projekt:\n%s", v)
	}
}

// Konflikt w TUI: okno wyboru, wybor "ich", enter -> scalenie.
func TestConflictFlow(t *testing.T) {
	a := project(t, "Ja", testdata(t, "base.als"))
	bdir := filepath.Join(t.TempDir(), "B")
	copyDir(t, filepath.Join(a.Dir, store.Dir), filepath.Join(bdir, store.Dir))
	b, err := store.Open(bdir, store.NewDevice("Kolega"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Pull(nil); err != nil {
		t.Fatal(err)
	}
	// kolega zmienia velocity nuty #8 w Hats, ja wczytuje theirs (inne nuty Hats)
	gz, _ := gzip.NewReader(bytes.NewReader(testdata(t, "base.als")))
	raw, _ := io.ReadAll(gz)
	raw = bytes.Replace(raw, []byte(`Velocity="70" OffVelocity="0" NoteId="8" />`), []byte(`Velocity="12" OffVelocity="0" NoteId="8" />`), 1)
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(raw)
	w.Close()
	os.WriteFile(b.WorkingPath(), buf.Bytes(), 0o644)
	if _, err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	shareDevice(t, b, a)
	os.WriteFile(a.WorkingPath(), testdata(t, "theirs.als"), 0o644)

	m := New(a)
	m = run(t, m, m.refreshCmd(true))
	if v := m.View(); !strings.Contains(v, "Kolega") || !strings.Contains(v, "ma nowe zmiany") && !strings.Contains(v, "do scalenia") {
		t.Fatalf("uczestnicy:\n%s", v)
	}
	m = key(t, m, "p")
	if m.conflict == nil {
		t.Fatalf("brak okna konfliktu:\n%s", m.View())
	}
	v := m.View()
	dump(t, v)
	if !strings.Contains(v, "Konflikt z Kolega") || !strings.Contains(v, `MidiTrack "Hats"`) || !strings.Contains(v, "zachowaj oba") {
		t.Fatalf("okno konfliktu:\n%s", v)
	}
	m = key(t, m, "enter") // bez wyboru nic sie nie dzieje
	if m.conflict == nil {
		t.Fatal("enter bez wyboru zamknal okno")
	}
	m = key(t, m, "t")
	m = key(t, m, "enter")
	if m.conflict != nil {
		t.Fatalf("okno nie zamkniete:\n%s", m.View())
	}
	dump(t, m.View())
	if v := m.View(); !strings.Contains(v, "Scalono") {
		t.Fatalf("po scaleniu:\n%s", v)
	}
	data, _ := os.ReadFile(a.WorkingPath())
	zr, _ := gzip.NewReader(bytes.NewReader(data))
	out, _ := io.ReadAll(zr)
	if !bytes.Contains(out, []byte(`Velocity="12" OffVelocity="0" NoteId="8"`)) {
		t.Error("wybrano zmiany kolegi, a nie ma ich w secie")
	}
}

// esc w oknie konfliktu zamyka je bez scalania i bez zmian w pliku.
func TestConflictCancel(t *testing.T) {
	p := project(t, "Ja", testdata(t, "base.als"))
	before, _ := os.ReadFile(p.WorkingPath())
	m := New(p)
	m.busy = ""
	m.conflict = &conflictState{with: store.Device{Name: "X"}, choice: map[string]als.Resolution{},
		list: []als.Conflict{{ID: "a", UnitLabel: "klip", CanBoth: true}}}
	m = key(t, m, "o")
	m = key(t, m, "esc")
	if m.conflict != nil || !strings.Contains(m.View(), "Scalanie przerwane") {
		t.Fatalf("po esc:\n%s", m.View())
	}
	if after, _ := os.ReadFile(p.WorkingPath()); !bytes.Equal(before, after) {
		t.Error("plik zmieniony mimo anulowania")
	}
}

// dump: TUI_DUMP=1 go test -v ./internal/tui pokazuje, jak wyglada ekran.
func dump(t *testing.T, view string) {
	if os.Getenv("TUI_DUMP") != "" {
		t.Log("\n" + view)
	}
}

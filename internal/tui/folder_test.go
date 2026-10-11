package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/c-airr/ableton-collab/store"
)

// session: folder sesji z setem w podfolderze (tak zapisuje Live przy
// "Save As" do zwyklego folderu) i samplem.
func session(t *testing.T, root string) string {
	dir := filepath.Join(root, "saves", "sesja")
	os.MkdirAll(filepath.Join(dir, "song Project", "Backup"), 0o755)
	os.MkdirAll(filepath.Join(dir, "Samples"), 0o755)
	os.WriteFile(filepath.Join(dir, "song Project", "song.als"), tinySet(), 0o644)
	os.WriteFile(filepath.Join(dir, "song Project", "Backup", "song [2026].als"), tinySet(), 0o644)
	os.WriteFile(filepath.Join(dir, "Samples", "bit.mp3"), []byte("mp3"), 0o644)
	return dir
}

func TestBrowseAndCreate(t *testing.T) {
	root := t.TempDir()
	dir := session(t, root)
	os.MkdirAll(filepath.Join(root, "abc"), 0o755) // "abc" przed "saves"
	be := &fakeBackend{t: t}
	s := started(t, be, StartOptions{Me: store.NewDevice("Ja"), BrowseFrom: root})
	s = press(t, s, "n")
	if s.stage != stBrowse || !strings.Contains(s.View(), "saves") {
		t.Fatalf("przegladarka:\n%s", s.View())
	}
	s = press(t, s, "down")
	s = press(t, s, "enter") // saves
	s = press(t, s, "enter") // sesja
	v := s.View()
	dump(t, v)
	if s.br.path != dir || !strings.Contains(v, "song Project") {
		t.Fatalf("jestem w %s:\n%s", s.br.path, v)
	}
	s = press(t, s, "left")
	s = press(t, s, "left")
	if s.br.path != root || s.br.dirs[s.br.cur] != "saves" {
		t.Fatalf("wyzej: %s, kursor %d", s.br.path, s.br.cur)
	}
	s = press(t, s, "enter")
	s = press(t, s, "enter")
	s = press(t, s, "s")
	if !s.Created() || s.Dir() != dir || be.created != "song Project/song.als" {
		t.Fatalf("created=%v dir=%q set=%q err=%v\n%s", s.Created(), s.Dir(), be.created, s.err, s.View())
	}
}

func TestBrowseNewFolderWaitsForSet(t *testing.T) {
	root := t.TempDir()
	be := &fakeBackend{t: t}
	s := started(t, be, StartOptions{Me: store.NewDevice("Ja"), BrowseFrom: root})
	s = press(t, s, "n")
	s = press(t, s, "+")
	s = press(t, s, "nowa sesja")
	s = press(t, s, "enter")
	dir := filepath.Join(root, "nowa sesja")
	if s.stage != stBrowse || s.br.path != dir {
		t.Fatalf("nowy folder: etap %d, %s, %v", s.stage, s.br.path, s.err)
	}
	s = press(t, s, "s")
	v := s.View()
	dump(t, v)
	if s.stage != stWaitSet || !strings.Contains(v, "Save Live Set As") {
		t.Fatalf("czekanie na set:\n%s", v)
	}
	// Live zapisuje set (najpierw niedokonczony plik - jeszcze nie bierzemy)
	os.MkdirAll(filepath.Join(dir, "beat Project"), 0o755)
	os.WriteFile(filepath.Join(dir, "beat Project", "beat.als"), tinySet()[:10], 0o644)
	s = drain(t, s, inspect(dir, true))
	if s.Created() {
		t.Fatal("wzieto niedokonczony set")
	}
	os.WriteFile(filepath.Join(dir, "beat Project", "beat.als"), tinySet(), 0o644)
	s = drain(t, s, inspect(dir, true))
	if !s.Created() || be.created != "beat Project/beat.als" {
		t.Fatalf("po zapisie: created=%v set=%q", s.Created(), be.created)
	}
}

func TestPickOneOfManySets(t *testing.T) {
	root := t.TempDir()
	dir := session(t, root)
	os.WriteFile(filepath.Join(dir, "inny.als"), tinySet(), 0o644)
	be := &fakeBackend{t: t}
	s := started(t, be, StartOptions{Me: store.NewDevice("Ja"), BrowseFrom: dir})
	s = press(t, s, "n")
	s = press(t, s, "s")
	if s.stage != stPickSet || len(s.info.Sets) != 2 {
		t.Fatalf("wybor setu: etap %d, %v", s.stage, s.info.Sets)
	}
	s = press(t, s, "down")
	s = press(t, s, "enter")
	if be.created != "song Project/song.als" {
		t.Fatalf("wybrano %q", be.created)
	}
}

func TestInspectFolder(t *testing.T) {
	root := t.TempDir()
	dir := session(t, root)
	info, err := InspectFolder(dir)
	if err != nil || len(info.Sets) != 1 || info.Files != 2 || info.Big() {
		t.Fatalf("%+v %v", info, err)
	}
	home, _ := os.UserHomeDir()
	for _, bad := range []string{home, filepath.Join(home, "Documents"), filepath.Dir(home), filepath.VolumeName(home) + string(os.PathSeparator)} {
		if _, err := InspectFolder(bad); err == nil {
			t.Errorf("pozwolono udostepnic %s", bad)
		}
	}
	// folder w srodku istniejacego projektu
	p := project(t, "Ja", tinySet())
	inner := filepath.Join(p.Dir, "Samples")
	os.MkdirAll(inner, 0o755)
	if _, err := InspectFolder(inner); err == nil || !strings.Contains(err.Error(), "innego projektu") {
		t.Errorf("folder w projekcie: %v", err)
	}
	if info, err := InspectFolder(p.Dir); err != nil || !info.Project {
		t.Errorf("istniejacy projekt: %+v %v", info, err)
	}
}

func TestForbiddenFolderInBrowser(t *testing.T) {
	home, _ := os.UserHomeDir()
	s := started(t, &fakeBackend{t: t}, StartOptions{Me: store.NewDevice("Ja"), BrowseFrom: home})
	s = press(t, s, "n")
	s = press(t, s, "s")
	if s.stage != stBrowse || s.err == nil || !strings.Contains(s.View(), "nie mozna udostepnic") {
		t.Fatalf("udostepniono folder domowy:\n%s", s.View())
	}
}

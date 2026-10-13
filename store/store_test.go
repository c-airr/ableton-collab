package store

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/c-airr/ableton-collab/als"
)

// Dwie osoby = dwa foldery projektu na jednym dysku. "Transport" to
// syncDirs: kopiuje .abcollab/ w obie strony (tak jak zrobi to Syncthing -
// kazdy pisze tylko w swoim devices/<id>/, wiec kopiowanie niczego nie nadpisze).

func testdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", name))
	if err != nil {
		t.Skip("brak testdata/" + name)
	}
	return data
}

type person struct {
	p   *Project
	dir string
}

func (x *person) write(t *testing.T, data []byte) {
	t.Helper()
	if err := os.WriteFile(x.p.WorkingPath(), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (x *person) working(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(x.p.WorkingPath())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (x *person) doc(t *testing.T) *als.Doc {
	t.Helper()
	d, err := als.Read(bytes.NewReader(x.working(t)))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func initPerson(t *testing.T, name string, set []byte) *person {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "song.als"), set, 0o644); err != nil {
		t.Fatal(err)
	}
	p, v, err := Init(dir, "song.als", NewDevice(name))
	if err != nil || v == nil {
		t.Fatalf("init: %v %v", v, err)
	}
	return &person{p, dir}
}

// joinPerson: druga osoba dostaje folder .abcollab od pierwszej (bez .als).
func joinPerson(t *testing.T, name string, from *person) *person {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	os.MkdirAll(dir, 0o755)
	copyTree(t, filepath.Join(from.dir, Dir), filepath.Join(dir, Dir))
	p, err := Open(dir, NewDevice(name))
	if err != nil {
		t.Fatal(err)
	}
	return &person{p, dir}
}

// syncDirs: katalog urzadzenia kopiujemy tylko od jego wlasciciela (jak
// Syncthing - nowsza wersja pliku wygrywa, a pisze go tylko wlasciciel).
func syncDirs(t *testing.T, a, b *person) {
	t.Helper()
	for _, x := range [][2]*person{{a, b}, {b, a}} {
		src, dst := x[0], x[1]
		own := filepath.Join(Dir, "devices", src.p.Me.ID)
		if _, err := os.Stat(filepath.Join(src.dir, own)); err == nil {
			copyTree(t, filepath.Join(src.dir, own), filepath.Join(dst.dir, own))
		}
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func mustPull(t *testing.T, x *person, res map[string]als.Resolution) *PullResult {
	t.Helper()
	r, err := x.p.Pull(res)
	if err != nil {
		t.Fatalf("pull (%s): %v", x.p.Me.Name, err)
	}
	return r
}

func onlyChanges(t *testing.T, a, b *als.Doc, want string) {
	t.Helper()
	ch := als.Diff(a, b)
	if len(ch) == 0 {
		t.Errorf("brak zmian, oczekiwano %q", want)
	}
	for _, c := range ch {
		if !strings.Contains(c.Label, want) {
			t.Errorf("nieoczekiwana zmiana (oczekiwano tylko %q): %s", want, c)
		}
	}
}

func TestInitAndCommit(t *testing.T) {
	a := initPerson(t, "A", testdata(t, "base.als"))
	if v, err := a.p.Commit(); err != nil || v != nil {
		t.Fatalf("commit bez zmian: %v %v", v, err)
	}
	root := a.p.Head()
	a.write(t, testdata(t, "theirs.als"))
	st, err := a.p.Status()
	if err != nil || len(st.Changes) == 0 {
		t.Fatalf("status powinien pokazac zmiany: %v %v", st, err)
	}
	v, err := a.p.Commit()
	if err != nil || v == nil {
		t.Fatalf("commit: %v %v", v, err)
	}
	if len(v.Parents) != 1 || v.Parents[0] != root || !strings.Contains(strings.Join(v.Summary, "\n"), "Hats") {
		t.Errorf("wersja: %+v", v)
	}
	if st, _ := a.p.Status(); len(st.Changes) != 0 {
		t.Errorf("po commicie dalej zmiany: %v", st.Changes)
	}
}

// Sam ponowny zapis (inne bajty gzip, ten sam projekt) nie tworzy wersji.
func TestCommitIgnoresReencode(t *testing.T) {
	a := initPerson(t, "A", testdata(t, "base.als"))
	gz, _ := gzip.NewReader(bytes.NewReader(testdata(t, "base.als")))
	raw, _ := io.ReadAll(gz)
	var b bytes.Buffer
	w, _ := gzip.NewWriterLevel(&b, gzip.BestSpeed)
	w.Write(raw)
	w.Close()
	a.write(t, b.Bytes())
	if v, err := a.p.Commit(); err != nil || v != nil {
		t.Fatalf("nowa wersja z samego przekodowania: %v %v", v, err)
	}
}

func TestJoinFastForward(t *testing.T) {
	a := initPerson(t, "A", testdata(t, "base.als"))
	b := joinPerson(t, "B", a)
	st, _ := b.p.Status()
	if len(st.Peers) != 1 || st.Peers[0].Relation != RelAhead {
		t.Fatalf("status B: %+v", st.Peers)
	}
	r := mustPull(t, b, nil)
	if len(r.FastForwarded) != 1 || !SameSet(b.working(t), testdata(t, "base.als")) || b.p.Head() != a.p.Head() {
		t.Fatalf("B nie dostal seta: %+v", r)
	}
}

// Glowny scenariusz: A dodaje gitare, B zmienia hihaty, obaj od tej samej bazy.
func TestDivergeAndMerge(t *testing.T) {
	a := initPerson(t, "A", testdata(t, "base.als"))
	b := joinPerson(t, "B", a)
	mustPull(t, b, nil)

	a.write(t, testdata(t, "ours.als"))
	b.write(t, testdata(t, "theirs.als"))
	// commit robi sam pull (automatycznie przed scalaniem) - tu jawnie dla B
	if v, err := b.p.Commit(); err != nil || v == nil {
		t.Fatal(err)
	}
	syncDirs(t, a, b)

	r := mustPull(t, a, nil)
	if r.Committed == nil || len(r.Merged) != 1 || len(r.Conflicts) > 0 {
		t.Fatalf("A pull: %+v", r)
	}
	m := r.Merged[0]
	if len(m.Parents) != 2 || m.Parents[0] != r.Committed.ID {
		t.Errorf("rodzice scalenia: %v", m.Parents)
	}
	ours, _ := als.Read(bytes.NewReader(testdata(t, "ours.als")))
	theirs, _ := als.Read(bytes.NewReader(testdata(t, "theirs.als")))
	onlyChanges(t, ours, a.doc(t), `MidiTrack "Hats"`)
	onlyChanges(t, theirs, a.doc(t), "8-12 String Guitar")

	syncDirs(t, a, b)
	st, _ := b.p.Status()
	if st.Peers[0].Relation != RelAhead {
		t.Errorf("B widzi A jako %v", st.Peers[0].Relation)
	}
	r = mustPull(t, b, nil)
	if len(r.FastForwarded) != 1 || len(r.Merged) != 0 {
		t.Fatalf("B pull: %+v", r)
	}
	if !SameSet(a.working(t), b.working(t)) {
		t.Error("A i B maja rozne pliki po synchronizacji")
	}
	syncDirs(t, a, b)
	for _, x := range []*person{a, b} {
		st, _ := x.p.Status()
		if st.Peers[0].Relation != RelSame || len(st.Changes) != 0 {
			t.Errorf("%s: %v zmiany=%d", x.p.Me.Name, st.Peers[0].Relation, len(st.Changes))
		}
	}
}

// edited: base z jedna recznie zmieniona velocity w klipie Hats (nuta #8) -
// koliduje z theirs.als, ktory zmienia nuty w tym samym klipie.
func edited(t *testing.T) []byte {
	gz, _ := gzip.NewReader(bytes.NewReader(testdata(t, "base.als")))
	raw, _ := io.ReadAll(gz)
	old := `Velocity="70" OffVelocity="0" NoteId="8" />`
	if bytes.Count(raw, []byte(old)) != 1 {
		t.Skip("base.als nie ma oczekiwanej nuty #8")
	}
	raw = bytes.Replace(raw, []byte(old), []byte(`Velocity="12" OffVelocity="0" NoteId="8" />`), 1)
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write(raw)
	w.Close()
	return b.Bytes()
}

func TestPullConflictThenResolve(t *testing.T) {
	a := initPerson(t, "A", testdata(t, "base.als"))
	b := joinPerson(t, "B", a)
	mustPull(t, b, nil)
	a.write(t, testdata(t, "theirs.als"))
	b.write(t, edited(t))
	b.p.Commit()
	syncDirs(t, a, b)

	r := mustPull(t, a, nil)
	if len(r.Conflicts) != 1 || r.ConflictWith.Name != "B" || r.WorkingChanged() {
		t.Fatalf("oczekiwany 1 konflikt z B: %+v", r)
	}
	headBefore := a.p.Head()
	if !SameSet(a.working(t), testdata(t, "theirs.als")) {
		t.Fatal("plik roboczy zmieniony mimo konfliktu")
	}
	r = mustPull(t, a, map[string]als.Resolution{r.Conflicts[0].ID: als.Theirs})
	if len(r.Merged) != 1 || r.Merged[0].Parents[0] != headBefore {
		t.Fatalf("po rozstrzygnieciu: %+v", r)
	}
	if !strings.Contains(string(a.doc(t).XMLBytes()), `Velocity="12" OffVelocity="0" NoteId="8"`) {
		t.Error("wybrano 'ich', a w wyniku nie ma zmiany B")
	}
}

// Pliki od drugiej strony dochodza po kawalku: head jest, obiektu jeszcze nie.
func TestIncompleteVersion(t *testing.T) {
	a := initPerson(t, "A", testdata(t, "base.als"))
	b := joinPerson(t, "B", a)
	mustPull(t, b, nil)
	a.write(t, testdata(t, "ours.als"))
	v, _ := a.p.Commit()
	syncDirs(t, a, b)
	obj := filepath.Join(b.dir, Dir, "devices", a.p.Me.ID, "objects", v.Object+".als")
	os.Remove(obj)

	st, _ := b.p.Status()
	if st.Peers[0].Relation != RelIncomplete {
		t.Fatalf("relacja: %v", st.Peers[0].Relation)
	}
	r := mustPull(t, b, nil)
	if len(r.Incomplete) != 1 || r.WorkingChanged() {
		t.Fatalf("pull przy niekompletnej wersji: %+v", r)
	}
	// obiekt uszkodzony (np. w trakcie zapisu) - tez nie wolno go uzyc
	os.WriteFile(obj, []byte("polowa pliku"), 0o644)
	if _, err := b.p.ObjectBytes(v); err == nil {
		t.Error("uszkodzony obiekt przeszedl weryfikacje")
	}
}

// Dolaczam z wlasnym, innym setem o tej samej nazwie - nie ginie bez kopii.
func TestJoinBackupsOwnSet(t *testing.T) {
	a := initPerson(t, "A", testdata(t, "base.als"))
	b := joinPerson(t, "B", a)
	b.write(t, testdata(t, "ours.als"))
	r := mustPull(t, b, nil)
	if r.Backup == "" {
		t.Fatal("brak kopii zapasowej")
	}
	if data, _ := os.ReadFile(r.Backup); !bytes.Equal(data, testdata(t, "ours.als")) {
		t.Error("kopia ma inna tresc")
	}
}

// Podrobiona wersja (tresc nie pasuje do ID) jest ignorowana.
func TestTamperedVersionIgnored(t *testing.T) {
	a := initPerson(t, "A", testdata(t, "base.als"))
	vs, _ := a.p.Versions()
	for id := range vs {
		path := filepath.Join(a.dir, Dir, "devices", a.p.Me.ID, "versions", id+".json")
		data, _ := os.ReadFile(path)
		os.WriteFile(path, bytes.Replace(data, []byte(`"parents": null`), []byte(`"parents": ["X"]`), 1), 0o644)
	}
	if vs, _ := a.p.Versions(); len(vs) != 0 {
		t.Errorf("podrobiona wersja przyjeta: %v", vs)
	}
}

// Criss-cross: dwa rownorzedne wspolne przodki - wybierany nowszy.
func TestMergeBase(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 1, 1, 0, m, 0, 0, time.UTC) }
	vs := map[string]*Version{
		"r":  {ID: "r", Time: at(0)},
		"a1": {ID: "a1", Parents: []string{"r"}, Time: at(1)},
		"b1": {ID: "b1", Parents: []string{"r"}, Time: at(2)},
		"a2": {ID: "a2", Parents: []string{"a1", "b1"}, Time: at(3)},
		"b2": {ID: "b2", Parents: []string{"b1", "a1"}, Time: at(4)},
		"a3": {ID: "a3", Parents: []string{"a2"}, Time: at(5)},
	}
	if got := MergeBase(vs, "a1", "b1"); got != "r" {
		t.Errorf("a1/b1: %s", got)
	}
	if got := MergeBase(vs, "a3", "a1"); got != "a1" {
		t.Errorf("przodek: %s", got)
	}
	if got := MergeBase(vs, "a3", "b2"); got != "b1" {
		t.Errorf("criss-cross: %s (oczekiwany nowszy z a1/b1)", got)
	}
	if !IsAncestor(vs, "r", "a3") || IsAncestor(vs, "a3", "r") {
		t.Error("IsAncestor")
	}
}

func TestValidIDRejectsPaths(t *testing.T) {
	for _, s := range []string{"", "..", "a/b", `a\b`, "a.b", strings.Repeat("x", 200)} {
		if validID(s) {
			t.Errorf("validID(%q) = true", s)
		}
	}
	if !validID(NewDevice("x").ID) {
		t.Error("ID urzadzenia odrzucone")
	}
}

// Set w podfolderze (Live przy "Save As" do zwyklego folderu zaklada
// "<nazwa> Project/"). U dolaczajacego podfolder jeszcze nie istnieje.
func TestSetInSubfolder(t *testing.T) {
	set := testdata(t, "base.als")
	dir := filepath.Join(t.TempDir(), "sesja")
	os.MkdirAll(filepath.Join(dir, "song Project"), 0o755)
	os.WriteFile(filepath.Join(dir, "song Project", "song.als"), set, 0o644)
	p, _, err := Init(dir, filepath.Join("song Project", "song.als"), NewDevice("Ja"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Meta.Set != "song Project/song.als" || p.SetName() != "song.als" {
		t.Fatalf("set zapisany jako %q", p.Meta.Set)
	}
	gdir := filepath.Join(t.TempDir(), "gosc")
	copyTree(t, filepath.Join(dir, Dir), filepath.Join(gdir, Dir))
	g, err := Open(gdir, NewDevice("Kolega"))
	if err != nil {
		t.Fatal(err)
	}
	if r, err := g.Pull(nil); err != nil || len(r.FastForwarded) != 1 {
		t.Fatalf("pull: %v %+v", err, r)
	}
	if got, _ := os.ReadFile(filepath.Join(gdir, "song Project", "song.als")); !SameSet(got, set) {
		t.Error("set goscia nie powstal w podfolderze")
	}
	for _, bad := range []string{"../x.als", `a\b.als`, "/x.als", "C:/x.als", ".abcollab/x.als", "a//b.als", "x.txt", "./x.als"} {
		if validSet(bad) {
			t.Errorf("przyjeto %q", bad)
		}
	}
}

// Dolaczanie z wlasna kopia: host ma theirs (zmienione hihaty), gosc ma
// u siebie ours (dodana gitara) - bez wspolnej historii w abcollab.
func TestAdoptOwnCopy(t *testing.T) {
	host := initPerson(t, "Host", testdata(t, "theirs.als"))
	g := joinPerson(t, "Gosc", host)
	local := g.p.WorkingPath()
	own := testdata(t, "ours.als")
	os.WriteFile(local, own, 0o644)

	r, err := g.p.Adopt(local, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Conflicts) != 1 || !strings.Contains(r.Conflicts[0].UnitLabel, "Hats") || r.TheirsTime.IsZero() || r.OursTime.IsZero() {
		t.Fatalf("konflikty: %+v", r.Conflicts)
	}
	if g.p.Head() != "" {
		t.Fatal("konflikt, a wersja zapisana")
	}
	r, err = g.p.Adopt(local, map[string]als.Resolution{r.Conflicts[0].ID: als.Theirs})
	if err != nil || len(r.Merged) != 1 || r.Backup == "" {
		t.Fatalf("%v %+v", err, r)
	}
	if b, _ := os.ReadFile(r.Backup); !bytes.Equal(b, own) {
		t.Error("brak kopii mojego seta sprzed scalenia")
	}
	doc := g.doc(t)
	xml := string(doc.XMLBytes())
	if !strings.Contains(xml, "8-12 String Guitar") {
		t.Error("zgubiona moja gitara")
	}
	if d := als.Diff(host.doc(t), doc); len(d) == 0 {
		t.Error("wynik identyczny z hostem - moje zmiany zgubione")
	}
	// host dostaje wynik jako zwykla nowa wersje (bez scalania)
	syncDirs(t, host, g)
	hr := mustPull(t, host, nil)
	if len(hr.FastForwarded) != 1 {
		t.Fatalf("host: %+v", hr)
	}
	if !SameSet(host.working(t), g.working(t)) {
		t.Error("host i gosc maja rozne sety")
	}
}

// Praca na zywo: Live ma otwarta stara wersje, zmiany kolegi przychodza
// (PullLive) bez podmiany pliku. Moj zapis z Live'a nie moze ich cofnac.
func TestLiveSaveKeepsOthersChanges(t *testing.T) {
	a := initPerson(t, "A", testdata(t, "base.als"))
	b := joinPerson(t, "B", a)
	mustPull(t, b, nil)
	b.write(t, testdata(t, "theirs.als")) // B zmienia hihaty
	if _, err := b.p.Commit(); err != nil {
		t.Fatal(err)
	}
	syncDirs(t, a, b)

	before := a.working(t)
	r, err := a.p.PullLive(nil)
	if err != nil || len(r.FastForwarded) != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	if !bytes.Equal(a.working(t), before) {
		t.Fatal("PullLive overwrote the working file")
	}
	if a.p.Loaded() == a.p.Head() {
		t.Fatal("Loaded moved with head")
	}
	if pr, _ := a.p.PendingReload(); len(pr) == 0 {
		t.Fatal("no pending changes to reload")
	}
	st, _ := a.p.Status()
	if len(st.Changes) != 0 {
		t.Fatalf("unchanged working file shows changes: %d", len(st.Changes))
	}

	// A w Live dodaje gitare (na bazie, bez hihatow B) i zapisuje
	a.write(t, testdata(t, "ours.als"))
	r, err = a.p.PullLive(nil)
	if err != nil || r.Committed == nil || len(r.Merged) != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	vs, _ := a.p.Versions()
	head, _ := a.p.LoadDoc(vs[a.p.Head()])
	xml := string(head.XMLBytes())
	if !strings.Contains(xml, "8-12 String Guitar") {
		t.Error("my guitar missing from head")
	}
	bdoc := b.doc(t)
	if d := als.Diff(bdoc, head); len(d) == 0 {
		t.Fatal("head equals B's version - my save was lost")
	}
	// zmiany B w head zostaly (diff wzgledem B to tylko moja gitara)
	for _, c := range als.Diff(bdoc, head) {
		if strings.Contains(c.Label, "Hats") {
			t.Errorf("my save reverted B's change: %s", c)
		}
	}
	if !bytes.Equal(a.working(t), testdata(t, "ours.als")) {
		t.Error("working file touched by the live merge")
	}
	if err := a.p.Reload(); err != nil {
		t.Fatal(err)
	}
	if a.p.Loaded() != a.p.Head() {
		t.Error("reload didn't set Loaded")
	}
}

// Folder projektu z Windowsa przychodzi jako "tylko do odczytu" (r-x) - pobranie
// seta musi go naprawic, a nie konczyc sie "permission denied".
func TestCheckoutIntoReadOnlyDir(t *testing.T) {
	set := testdata(t, "base.als")
	dir := filepath.Join(t.TempDir(), "sesja")
	os.MkdirAll(filepath.Join(dir, "x Project"), 0o755)
	os.WriteFile(filepath.Join(dir, "x Project", "x.als"), set, 0o644)
	if _, _, err := Init(dir, "x Project/x.als", NewDevice("Ja")); err != nil {
		t.Fatal(err)
	}
	gdir := filepath.Join(t.TempDir(), "gosc")
	copyTree(t, filepath.Join(dir, Dir), filepath.Join(gdir, Dir))
	os.MkdirAll(filepath.Join(gdir, "x Project"), 0o755)
	os.Chmod(filepath.Join(gdir, "x Project"), 0o555)
	defer os.Chmod(filepath.Join(gdir, "x Project"), 0o755)
	g, err := Open(gdir, NewDevice("Kolega"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Pull(nil); err != nil {
		t.Fatalf("pull into a read-only project dir: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(gdir, "x Project", "x.als")); !SameSet(got, set) {
		t.Fatal("set not written")
	}
}

// Reload podmienia plik, ale Live go nie przeladowal i zapisuje stara tresc
// (z nowym plikiem nadpisanym) - zapis nie moze cofnac zmian innych.
func TestSaveFromStaleLiveAfterReload(t *testing.T) {
	for _, marked := range []bool{false, true} {
		a := initPerson(t, "A", testdata(t, "base.als"))
		b := joinPerson(t, "B", a)
		mustPull(t, b, nil)
		// Live A ma otwarta wersje startowa: bez znacznika (set sprzed
		// znacznikow) albo ze znacznikiem z wczesniejszego pobrania
		stale := testdata(t, "ours.als") // A dodaje gitare
		if marked {
			if err := a.p.Reload(); err != nil {
				t.Fatal(err)
			}
			var err error
			if stale, err = withBase(stale, a.p.Head()); err != nil || stale == nil {
				t.Fatal("no marker", err)
			}
		}
		b.write(t, testdata(t, "theirs.als")) // B zmienia hihaty
		if _, err := b.p.Commit(); err != nil {
			t.Fatal(err)
		}
		syncDirs(t, a, b)
		if _, err := a.p.PullLive(nil); err != nil {
			t.Fatal(err)
		}
		if err := a.p.Reload(); err != nil {
			t.Fatal(err)
		}
		// Live nie wczytal nowego pliku - Ctrl+S zapisuje to, co ma w pamieci
		a.write(t, stale)
		r, err := a.p.PullLive(nil)
		if err != nil || r.Committed == nil || len(r.Conflicts) > 0 {
			t.Fatalf("marked=%v: %v %+v", marked, err, r)
		}
		vs, _ := a.p.Versions()
		if got := vs[r.Committed.ID].Parents; len(got) != 1 || got[0] == b.p.Head() {
			t.Errorf("marked=%v: stale save got the reloaded version as parent", marked)
		}
		head, _ := a.p.LoadDoc(vs[a.p.Head()])
		if !strings.Contains(string(head.XMLBytes()), "8-12 String Guitar") {
			t.Errorf("marked=%v: my guitar missing", marked)
		}
		bv, _ := b.p.LoadDoc(vs[b.p.Head()])
		for _, c := range als.Diff(bv, head) {
			if strings.Contains(c.Label, "Hats") {
				t.Errorf("marked=%v: stale save reverted B's change: %s", marked, c)
			}
		}
		// Live w koncu wczytuje plik (ze znacznikiem head) - kolejny zapis
		// idzie juz od head, bez scalania
		if err := a.p.Reload(); err != nil {
			t.Fatal(err)
		}
		before := a.p.Head()
		d := a.doc(t)
		if docBase(d) != before {
			t.Fatalf("marked=%v: reloaded file has marker %q, want head", marked, docBase(d))
		}
		d.Root.Child("LiveSet").Child("Tracks").Children[0].Find("Name", "EffectiveName").SetAttr("Value", "Renamed")
		data, _ := d.Encode()
		a.write(t, data)
		r, err = a.p.PullLive(nil)
		if err != nil || r.Committed == nil || len(r.Merged) != 0 || vs[before] == nil || r.Committed.Parents[0] != before {
			t.Fatalf("marked=%v: save after reload: %v %+v", marked, err, r)
		}
	}
}

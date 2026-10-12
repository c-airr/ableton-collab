package tui

import (
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/c-airr/ableton-collab/als"
	"github.com/c-airr/ableton-collab/store"
	"github.com/c-airr/ableton-collab/transport/syncthing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const (
	idHost  = "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	idGuest = "KS776L4-HIZCKQU-TMCZNZ2-TY3Q6H4-OIK6PPP-3JYMQ4F-4XLB6WR-LBGQEAR"
	idOther = "ZZZZZZZ-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
)

// --- atrapy ----------------------------------------------------------------------------------

type fakeNet struct {
	lobby                      syncthing.Lobby
	accepted, rejected, kicked []string
	events                     []syncthing.Event
}

func (f *fakeNet) Lobby() (*syncthing.Lobby, error) { l := f.lobby; return &l, nil }
func (f *fakeNet) Invite(name string) string        { return "ABC1KOD" + strings.ToUpper(name) }
func (f *fakeNet) Accept(r syncthing.Request) error {
	f.accepted = append(f.accepted, r.Name)
	f.drop(r.ID)
	f.lobby.Members = append(f.lobby.Members, syncthing.Member{ID: r.ID, Name: r.Name})
	return nil
}
func (f *fakeNet) Reject(r syncthing.Request) error {
	f.rejected = append(f.rejected, r.Name)
	f.drop(r.ID)
	return nil
}
func (f *fakeNet) Kick(m syncthing.Member) error {
	f.kicked = append(f.kicked, m.Name)
	var keep []syncthing.Member
	for _, x := range f.lobby.Members {
		if x.ID != m.ID {
			keep = append(keep, x)
		}
	}
	f.lobby.Members = keep
	return nil
}
func (f *fakeNet) Events(since int, _ time.Duration) ([]syncthing.Event, int, error) {
	evs := f.events
	f.events = nil
	return evs, since + len(evs), nil
}
func (f *fakeNet) drop(id string) {
	var keep []syncthing.Request
	for _, r := range f.lobby.Requests {
		if r.ID != id {
			keep = append(keep, r)
		}
	}
	f.lobby.Requests = keep
}

// fakeBackend: prawdziwy store, bez Syncthinga. Join "dostarcza" sesje hosta
// dopiero na wywolanie deliver() - jak Syncthing po akceptacji.
type fakeBackend struct {
	t        *testing.T
	me       store.Device
	net      *fakeNet
	host     *store.Project // sesja, do ktorej sie dolacza
	joined   string
	existing bool
	renamed  string
	last     string
	sessions []SessionRef
	live     LiveSync
	settings Settings
	saves    int
}

func (b *fakeBackend) Live(p *store.Project) LiveSync { return b.live }
func (b *fakeBackend) Settings() Settings             { return b.settings }
func (b *fakeBackend) SetSettings(s Settings) error   { b.settings = s; return nil }
func (b *fakeBackend) SaveInLive() (bool, error)      { b.saves++; return true, nil }

func (b *fakeBackend) DeviceID() string { return idGuest }
func (b *fakeBackend) Rename(n string) error {
	b.renamed, b.me.Name = n, n
	return nil
}
func (b *fakeBackend) Sessions() ([]SessionRef, error) { return b.sessions, nil }
func (b *fakeBackend) Open(dir string) (*store.Project, Network, error) {
	p, err := store.Open(dir, b.me)
	if err != nil {
		return nil, nil, err
	}
	return p, b.net, nil
}
func (b *fakeBackend) Host(dir, set string) (*store.Project, Network, error) {
	p, _, err := store.Init(dir, set, b.me)
	if err != nil {
		return nil, nil, err
	}
	return p, b.net, nil
}
func (b *fakeBackend) Join(inv syncthing.Invite, dir string, existing bool) (Network, error) {
	if ents, _ := os.ReadDir(dir); len(ents) > 0 && !existing {
		return nil, errors.New("folder not empty")
	}
	os.MkdirAll(dir, 0o755)
	b.joined, b.existing = dir, existing
	return &fakeNet{lobby: syncthing.Lobby{Members: []syncthing.Member{{ID: inv.Device, Name: inv.Host, Connected: true}}}}, nil
}
func (b *fakeBackend) SetLast(dir string) { b.last = dir }

// deliver: Syncthing przyniosl historie hosta do folderu goscia.
func (b *fakeBackend) deliver() {
	copyDir(b.t, filepath.Join(b.host.Dir, store.Dir), filepath.Join(b.joined, store.Dir))
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

func tinySet() []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write([]byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\r\n<Ableton MajorVersion=\"5\">\r\n\t<LiveSet>\r\n\t\t<NextPointeeId Value=\"10\" />\r\n\t\t<Tracks />\r\n\t</LiveSet>\r\n</Ableton>\r\n"))
	w.Close()
	return buf.Bytes()
}

func testdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Skip("no testdata/" + name)
	}
	return data
}

// session: folder sesji z setem w podfolderze (tak zapisuje Live) i samplem.
func sessionDir(t *testing.T, set []byte) string {
	dir := filepath.Join(t.TempDir(), "beat")
	os.MkdirAll(filepath.Join(dir, "beat Project"), 0o755)
	os.MkdirAll(filepath.Join(dir, "Samples"), 0o755)
	os.WriteFile(filepath.Join(dir, "beat Project", "beat.als"), set, 0o644)
	os.WriteFile(filepath.Join(dir, "Samples", "hihat.wav"), []byte("wav"), 0o644)
	return dir
}

func noClipboard(t *testing.T) {
	old := clipboard
	clipboard = func(string) error { return errors.New("test") }
	t.Cleanup(func() { clipboard = old })
}

// --- sterowanie ------------------------------------------------------------------------------

func newApp(t *testing.T, be *fakeBackend, o Options) App {
	noClipboard(t)
	if o.Me.ID == "" {
		o.Me = be.me
	}
	a := New(be, o)
	a.w, a.h = 120, 34
	a.pollEvery = time.Millisecond
	return drive(t, a, a.Init())
}

// drive wykonuje komendy i podaje wyniki do Update (bez tickow i zdarzen,
// ktore krecilyby sie w nieskonczonosc).
func drive(t *testing.T, a App, cmd tea.Cmd) App {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0 && steps < 200; steps++ {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := c()
		switch m := msg.(type) {
		case tea.BatchMsg:
			queue = append(queue, m...)
			continue
		case tickMsg, eventsMsg, tea.QuitMsg:
			continue
		}
		next, nc := a.Update(msg)
		a = next.(App)
		queue = append(queue, nc)
	}
	return a
}

func press(t *testing.T, a App, keys ...string) App {
	t.Helper()
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "left":
			msg = tea.KeyMsg{Type: tea.KeyLeft}
		case "right":
			msg = tea.KeyMsg{Type: tea.KeyRight}
		case "ctrl+u":
			msg = tea.KeyMsg{Type: tea.KeyCtrlU}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k), Paste: len([]rune(k)) > 1}
		}
		next, cmd := a.Update(msg)
		a = drive(t, next.(App), cmd)
	}
	return a
}

func tickApp(t *testing.T, a App) App {
	next, cmd := a.Update(tickMsg{})
	return drive(t, next.(App), cmd)
}

func view(a App) string { return ansi.Strip(a.View()) }

func dump(t *testing.T, a App) {
	if os.Getenv("TUI_DUMP") != "" {
		t.Log("\n" + view(a))
	}
}

func must(t *testing.T, a App, want ...string) {
	t.Helper()
	v := view(a)
	for _, w := range want {
		if !strings.Contains(v, w) {
			t.Fatalf("no %q on screen:\n%s", w, v)
		}
	}
}

// --- testy -----------------------------------------------------------------------------------

func TestEmptyPanel(t *testing.T) {
	be := &fakeBackend{t: t, me: store.NewDevice("Ja"), net: &fakeNet{}}
	a := newApp(t, be, Options{})
	dump(t, a)
	must(t, a, "FILES", "PEOPLE", "LOG", "ACTIONS", "Join or host a session", "Join", "Host", "Settings", "no session")
	if v := view(a); !strings.Contains(v, "▄▀█ █▄▄") {
		t.Fatalf("no logo:\n%s", v)
	}
}

func TestFirstRunAsksNickname(t *testing.T) {
	be := &fakeBackend{t: t, me: store.NewDevice("DESKTOP-1"), net: &fakeNet{}}
	a := newApp(t, be, Options{AskName: true})
	must(t, a, "Welcome", "nickname", "DESKTOP-1")
	a = press(t, a, "ctrl+u", "bartek", "enter")
	if be.renamed != "bartek" || a.dlg != nil || a.me.Name != "bartek" {
		t.Fatalf("renamed %q, dialog %v", be.renamed, a.dlg)
	}
}

func TestOpensLastSession(t *testing.T) {
	me := store.NewDevice("Ja")
	me.Peer = idHost
	dir := sessionDir(t, tinySet())
	if _, _, err := store.Init(dir, "beat Project/beat.als", me); err != nil {
		t.Fatal(err)
	}
	net := &fakeNet{lobby: syncthing.Lobby{Me: idHost,
		Members:  []syncthing.Member{{ID: idOther, Name: "Basia", Connected: true, Via: "relay"}},
		Requests: []syncthing.Request{{ID: idGuest, Name: "Kolega", Time: time.Now()}}}}
	be := &fakeBackend{t: t, me: me, net: net}
	a := newApp(t, be, Options{Last: dir})
	dump(t, a)
	must(t, a, "FILES · beat", "beat Project", "beat.als ◆", "Samples", "Ja  you, host", "Basia", "relay",
		"Wants to join", "Kolega", "KS776L4", "Kolega wants to join", "Opened session beat", "Save", "Get changes", "1 of 1 online")
	if be.last != dir {
		t.Errorf("last session not remembered: %q", be.last)
	}
	// prosba: zaznacz w PEOPLE i Accept
	a = press(t, a, "tab") // FILES -> PEOPLE
	a = press(t, a, "down")
	must(t, a, "Accept", "Reject", "selected: Kolega")
	a = press(t, a, "a")
	if len(net.accepted) != 1 {
		t.Fatalf("accepted %v", net.accepted)
	}
	must(t, a, "Kolega accepted")
	// kick (jestem hostem)
	a = press(t, a, "up")
	must(t, a, "Kick", "selected: Basia")
	a = press(t, a, "k")
	must(t, a, "Kick Basia?")
	a = press(t, a, "up", "enter")
	if len(net.kicked) != 1 || net.kicked[0] != "Basia" {
		t.Fatalf("kicked %v", net.kicked)
	}
}

func TestDeleteFile(t *testing.T) {
	me := store.NewDevice("Ja")
	dir := sessionDir(t, tinySet())
	store.Init(dir, "beat Project/beat.als", me)
	be := &fakeBackend{t: t, me: me, net: &fakeNet{}}
	a := newApp(t, be, Options{Last: dir})
	// set sesji nie da sie usunac
	if strings.Contains(view(a), "Delete ") {
		t.Fatal("the session set can be deleted")
	}
	a.ses.tree.sel = "Samples/hihat.wav"
	a.ses.tree.open["Samples"] = true
	must(t, a, "hihat.wav", "Delete ")
	a = press(t, a, "x")
	must(t, a, "Delete Samples/hihat.wav?")
	a = press(t, a, "up", "enter")
	if _, err := os.Stat(filepath.Join(dir, "Samples", "hihat.wav")); err == nil {
		t.Fatal("file not deleted")
	}
	mustLog(t, a, "You deleted Samples/hihat.wav")
}

func TestHostExistingSet(t *testing.T) {
	dir := sessionDir(t, tinySet())
	be := &fakeBackend{t: t, me: store.NewDevice("Ja"), net: &fakeNet{}}
	a := newApp(t, be, Options{BrowseFrom: filepath.Dir(dir)})
	a = press(t, a, "h")
	must(t, a, "Host a session", "New folder", "Existing folder")
	a = press(t, a, "down", "enter")
	must(t, a, "Pick the folder with your project", "beat"+sep)
	a = press(t, a, "tab") // wejdz w podpowiedz "beat"
	if a.dlg.(*hostDialog).path.value != dir+sep {
		t.Fatalf("path %q", a.dlg.(*hostDialog).path.value)
	}
	a = press(t, a, "enter")
	dump(t, a)
	if a.ses == nil || a.ses.p.Meta.Set != "beat Project/beat.als" {
		t.Fatalf("no session:\n%s", view(a))
	}
	must(t, a, "Invite to beat", "ABC1KODJA")
}

func TestHostWaitsForLive(t *testing.T) {
	root := t.TempDir()
	be := &fakeBackend{t: t, me: store.NewDevice("Ja"), net: &fakeNet{}}
	a := newApp(t, be, Options{BrowseFrom: root})
	a = press(t, a, "h", "enter") // New folder - pole ma juz "<start>/my session"
	if v := a.dlg.(*hostDialog).path.Path(); v != filepath.Join(root, "my session") {
		t.Fatalf("new folder prefilled with %q", v)
	}
	a = press(t, a, "enter") // nie istnieje - zostanie zalozony
	dump(t, a)
	must(t, a, "Save your project from Live", "Save Live Set As", "waiting for an .als")
	dir := filepath.Join(root, "my session")
	os.MkdirAll(filepath.Join(dir, "x Project"), 0o755)
	os.WriteFile(filepath.Join(dir, "x Project", "x.als"), tinySet()[:12], 0o644) // Live jeszcze zapisuje
	a = tickApp(t, a)
	if a.ses != nil {
		t.Fatal("took a half-written set")
	}
	os.WriteFile(filepath.Join(dir, "x Project", "x.als"), tinySet(), 0o644)
	a = tickApp(t, a)
	if a.ses == nil || a.ses.p.Meta.Set != "x Project/x.als" {
		t.Fatalf("no session:\n%s", view(a))
	}
}

func TestHostForbiddenFolder(t *testing.T) {
	home, _ := os.UserHomeDir()
	be := &fakeBackend{t: t, me: store.NewDevice("Ja"), net: &fakeNet{}}
	a := newApp(t, be, Options{BrowseFrom: home})
	a = press(t, a, "h", "down", "enter", "enter")
	must(t, a, "you can't share this folder")
}

func hostProject(t *testing.T, set []byte) *store.Project {
	me := store.NewDevice("Host")
	me.Peer = idHost
	dir := sessionDir(t, set)
	p, _, err := store.Init(dir, "beat Project/beat.als", me)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestJoinNewFolder(t *testing.T) {
	host := hostProject(t, tinySet())
	base := t.TempDir()
	be := &fakeBackend{t: t, me: store.NewDevice("Kolega"), net: &fakeNet{}, host: host}
	a := newApp(t, be, Options{BaseDir: base})
	a = press(t, a, "j")
	must(t, a, "Join a session", "ABC1")
	a = press(t, a, "smieci", "enter")
	must(t, a, "not an abcollab invite code")
	code := syncthing.Invite{Device: idHost, Folder: "abcollab-abc", Name: "beat", Host: "Host"}.Code()
	a = press(t, a, "ctrl+u", code[:40]+"\n"+code[40:], "enter")
	must(t, a, "Join beat (hosted by Host)", "New folder", "Existing folder")
	a = press(t, a, "enter") // New folder
	want := filepath.Join(base, "beat")
	a = press(t, a, "enter")
	if be.joined != want || be.existing {
		t.Fatalf("joined %q existing %v\n%s", be.joined, be.existing, view(a))
	}
	dump(t, a)
	must(t, a, "Joining beat", "KS776L4", "folder:")
	be.deliver()
	a = tickApp(t, a)
	if a.ses == nil {
		t.Fatalf("no session after delivery:\n%s", view(a))
	}
	if got, _ := os.ReadFile(filepath.Join(want, "beat Project", "beat.als")); !bytes.Equal(got, tinySet()) {
		t.Error("set not downloaded")
	}
}

func TestJoinExistingMerge(t *testing.T) {
	host := hostProject(t, testdata(t, "theirs.als"))
	mine := sessionDir(t, testdata(t, "ours.als"))
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(mine, "beat Project", "beat.als"), old, old) // moj set starszy
	be := &fakeBackend{t: t, me: store.NewDevice("Kolega"), net: &fakeNet{}, host: host}
	code := syncthing.Invite{Device: idHost, Folder: "abcollab-abc", Name: "beat", Host: "Host"}.Code()
	a := newApp(t, be, Options{JoinCode: code, BrowseFrom: filepath.Dir(mine)})
	must(t, a, "Join beat")
	a = press(t, a, "down", "enter") // Existing folder
	a = press(t, a, "tab", "enter")  // beat/ i zatwierdz
	must(t, a, "This folder already has files", "Merge files", "Replace with the host's files")
	a = press(t, a, "enter")
	if be.joined != mine || !be.existing {
		t.Fatalf("joined %q existing %v", be.joined, be.existing)
	}
	be.deliver()
	a = tickApp(t, a)
	dump(t, a)
	must(t, a, "Conflict with Host", "Hats", "YOURS", "HOST'S", "Keep Host's  (newer)")
	if _, ok := a.dlg.(*conflictDialog); !ok {
		t.Fatal("no conflict dialog")
	}
	a = press(t, a, "enter") // zaznaczona nowsza - hosta
	a = tickApp(t, a)
	if a.ses == nil {
		t.Fatalf("no session after merge:\n%s", view(a))
	}
	got, _ := os.ReadFile(filepath.Join(mine, "beat Project", "beat.als"))
	if !bytes.Contains(gunzip(t, got), []byte("8-12 String Guitar")) {
		t.Error("my guitar was lost in the merge")
	}
}

func gunzip(t *testing.T, data []byte) []byte {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	b.ReadFrom(r)
	return b.Bytes()
}

func TestJoinExistingReplace(t *testing.T) {
	host := hostProject(t, tinySet())
	junk := filepath.Join(t.TempDir(), "smietnik")
	os.MkdirAll(junk, 0o755)
	os.WriteFile(filepath.Join(junk, "stare.txt"), []byte("x"), 0o644)
	be := &fakeBackend{t: t, me: store.NewDevice("Kolega"), net: &fakeNet{}, host: host}
	code := syncthing.Invite{Device: idHost, Folder: "abcollab-abc", Name: "beat", Host: "Host"}.Code()
	a := newApp(t, be, Options{JoinCode: code, BrowseFrom: junk})
	a = press(t, a, "down", "enter", "enter")
	must(t, a, "Replace with the host's files", "DELETES everything")
	if strings.Contains(view(a), "Merge files") {
		t.Fatal("merge offered without a set")
	}
	a = press(t, a, "enter")
	must(t, a, "Delete everything in this folder?", "can't be undone")
	a = press(t, a, "up", "enter") // domyslnie zaznaczone "Back"
	if _, err := os.Stat(filepath.Join(junk, "stare.txt")); err == nil {
		t.Fatal("folder not cleared")
	}
	if be.joined != junk || be.existing {
		t.Fatalf("joined %q existing %v", be.joined, be.existing)
	}
}

func TestPathInput(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "code", "projects"), 0o755)
	os.MkdirAll(filepath.Join(root, "cache"), 0o755)
	os.MkdirAll(filepath.Join(root, "music"), 0o755)
	p := newPathInput(root)
	if len(p.sugg) != 3 {
		t.Fatalf("suggestions %v", p.sugg)
	}
	p.value += "co"
	p.refresh()
	if len(p.sugg) != 1 || p.ghost() != "de" {
		t.Fatalf("suggestions %v ghost %q", p.sugg, p.ghost())
	}
	// enter na nieistniejacym "co" z jedna podpowiedzia wchodzi w "code"
	if submit, _ := p.key(tea.KeyMsg{Type: tea.KeyEnter}); submit || p.value != filepath.Join(root, "code")+sep {
		t.Fatalf("value %q submit %v", p.value, submit)
	}
	if len(p.sugg) != 1 || filepath.Base(p.sugg[0]) != "projects" {
		t.Fatalf("children %v", p.sugg)
	}
	// enter bez podswietlenia zatwierdza istniejacy folder
	if submit, _ := p.key(tea.KeyMsg{Type: tea.KeyEnter}); !submit || p.Path() != filepath.Join(root, "code") {
		t.Fatalf("submit %v path %q", submit, p.Path())
	}
}

func TestLogAndHistory(t *testing.T) {
	me := store.NewDevice("Ja")
	dir := sessionDir(t, tinySet())
	store.Init(dir, "beat Project/beat.als", me)
	net := &fakeNet{}
	be := &fakeBackend{t: t, me: me, net: net}
	a := newApp(t, be, Options{Last: dir})
	net.events = []syncthing.Event{
		{Kind: "received", File: "Samples/kick.wav"}, {Kind: "sending", File: "Samples/hihat.wav"}, {Kind: "connected", Who: "Kolega"}}
	// zdarzenia przychodza z long-polla
	next, cmd := a.Update(a.ses.eventsCmd(a.sid, 0)())
	a = drive(t, next.(App), cmd)
	mustLog(t, a, "kick.wav received", "hihat.wav → sending to others", "Kolega connected")
	must(t, a, "kick.wav received")
	a = press(t, a, "y")
	must(t, a, "HISTORY", "first version", " Log ")
}

func TestVersionGraph(t *testing.T) {
	v := func(id string, parents ...string) *store.Version { return &store.Version{ID: id, Parents: parents} }
	// m scala a i b, ktore wyszly z root
	g := versionGraph([]*store.Version{v("m", "a", "b"), v("b", "root"), v("a", "root"), v("root")})
	want := []string{"◆   ", "│ ● ", "● │ ", "●   "}
	for i := range want {
		if strings.TrimRight(g[i], " ") != strings.TrimRight(want[i], " ") {
			t.Fatalf("graph:\n%s", strings.Join(g, "\n"))
		}
	}
}

func TestMouseButtons(t *testing.T) {
	be := &fakeBackend{t: t, me: store.NewDevice("Ja"), net: &fakeNet{}}
	a := newApp(t, be, Options{})
	l := a.layout()
	r := l.btns[1] // Host
	next, cmd := a.Update(tea.MouseMsg{X: r.x + 2, Y: r.y + 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	a = drive(t, next.(App), cmd)
	if _, ok := a.dlg.(*hostDialog); !ok {
		t.Fatalf("click on Host didn't open it:\n%s", view(a))
	}
}

func TestInspectFolder(t *testing.T) {
	dir := sessionDir(t, tinySet())
	info, err := InspectFolder(dir)
	if err != nil || len(info.Sets) != 1 || info.Sets[0] != "beat Project/beat.als" || info.Files != 2 || info.Big() {
		t.Fatalf("%+v %v", info, err)
	}
	home, _ := os.UserHomeDir()
	for _, bad := range []string{home, filepath.Join(home, "Documents"), filepath.Dir(home), filepath.VolumeName(home) + sep} {
		if _, err := InspectFolder(bad); err == nil {
			t.Errorf("allowed to share %s", bad)
		}
	}
}

func TestPianoRoll(t *testing.T) {
	d, err := als.Read(bytes.NewReader(testdata(t, "base.als")))
	if err != nil {
		t.Fatal(err)
	}
	var clip *als.Node
	d.Root.Walk(func(n *als.Node) {
		if clip == nil && n.Name == "MidiClip" {
			clip = n
		}
	})
	lines := pianoRoll(clip, 40, 8)
	if len(lines) < 2 || !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "notes") {
		t.Fatalf("roll:\n%s", strings.Join(lines, "\n"))
	}
	if os.Getenv("TUI_DUMP") != "" {
		t.Log("\n" + strings.Join(lines, "\n"))
	}
}

// mustLog: wpis w logu sesji (na ekranie dlugie wpisy sa zawijane).
func mustLog(t *testing.T, a App, want ...string) {
	t.Helper()
	var all []string
	for _, l := range a.ses.log {
		all = append(all, l.text)
	}
	joined := strings.Join(all, "\n")
	for _, w := range want {
		if !strings.Contains(joined, w) {
			t.Fatalf("no %q in log:\n%s", w, joined)
		}
	}
}

// Wklejona pelna sciezka zastepuje zawartosc pola (wczesniej doklejala sie
// do "<Dokumenty>\Ableton\" i mkdir konczyl sie bledem), smieci z wklejania
// sa wycinane.
func TestPastePathReplaces(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "beat")
	os.MkdirAll(target, 0o755)
	p := newPathInput(filepath.Join(root, "start"))
	p.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("\x1b[200~\"" + target + "\"\r\n\x00"), Paste: true})
	if p.Path() != target {
		t.Fatalf("pasted path: %q", p.Path())
	}
	// pisanie po literze dalej dokleja
	p.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if !strings.HasSuffix(p.value, "x") || !strings.HasPrefix(p.value, target) {
		t.Fatalf("typing: %q", p.value)
	}
	if runtime.GOOS == "windows" {
		glued := &pathInput{cur: -1}
		glued.value = `C:\Docs\Ableton\` + target
		if glued.Path() != target {
			t.Fatalf("glued path: %q", glued.Path())
		}
		if PathProblem(`C:\a\b?c`) == "" || PathProblem(target) != "" {
			t.Fatal("PathProblem")
		}
	}
}

func TestHostNewFolderMustBeEmpty(t *testing.T) {
	root := t.TempDir()
	full := filepath.Join(root, "full")
	os.MkdirAll(full, 0o755)
	os.WriteFile(filepath.Join(full, "x.txt"), []byte("x"), 0o644)
	be := &fakeBackend{t: t, me: store.NewDevice("Ja"), net: &fakeNet{}}
	a := newApp(t, be, Options{BrowseFrom: root})
	a = press(t, a, "h", "enter", "ctrl+u", full, "enter")
	must(t, a, "already has files")
}

func TestHostExistingNeedsSet(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "samples only"), 0o755)
	os.WriteFile(filepath.Join(root, "samples only", "kick.wav"), []byte("x"), 0o644)
	be := &fakeBackend{t: t, me: store.NewDevice("Ja"), net: &fakeNet{}}
	a := newApp(t, be, Options{BrowseFrom: root})
	a = press(t, a, "h", "down", "enter", "tab", "enter")
	must(t, a, "no Live set (.als) in this folder")
	if a.ses != nil {
		t.Fatal("hosted a folder without a set")
	}
}

package live

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeLive: atrapa skryptu AbletonCollab - stan w pamieci, ten sam protokol.
type fakeLive struct {
	t      *testing.T
	ln     net.Listener
	mu     sync.Mutex
	conn   net.Conn
	tracks []string
	items  map[string]Obj
	token  string
}

func newFakeLive(t *testing.T, tracks []string, items map[string]Obj) *fakeLive {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeLive{t: t, ln: ln, tracks: tracks, items: items, token: "tok"}
	t.Cleanup(func() { ln.Close() })
	go f.serve()
	return f
}

func (f *fakeLive) addr() string { return f.ln.Addr().String() }

func (f *fakeLive) send(m any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conn == nil {
		return
	}
	data, _ := json.Marshal(m)
	f.conn.Write(append(data, '\n'))
}

func (f *fakeLive) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conn = c
		f.mu.Unlock()
		sc := bufio.NewScanner(c)
		for sc.Scan() {
			var m map[string]any
			json.Unmarshal(sc.Bytes(), &m)
			switch m["op"] {
			case "hello":
				f.send(map[string]any{"t": "hello", "tracks": f.tracks})
			case "state":
				f.mu.Lock()
				items := map[string]Obj{}
				for k, v := range f.items {
					items[k] = v
				}
				f.mu.Unlock()
				f.send(map[string]any{"t": "state", "id": m["id"], "tracks": f.tracks, "items": items})
			case "apply":
				key := m["key"].(string)
				obj := Obj(m["obj"].(map[string]any))
				f.mu.Lock()
				f.items[key] = obj
				f.mu.Unlock()
				f.send(map[string]any{"t": "applied", "id": m["id"], "key": key, "ok": true})
			}
		}
	}
}

// edit: uzytkownik zmienia cos w Live (skrypt zglasza zmiane).
func (f *fakeLive) edit(key string, obj Obj) {
	f.mu.Lock()
	f.items[key] = obj
	f.mu.Unlock()
	f.send(map[string]any{"t": "change", "key": key, "obj": obj})
}

func (f *fakeLive) get(key string) Obj {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.items[key]
}

func clip(notes ...float64) Obj {
	var ns []any
	for _, p := range notes {
		ns = append(ns, []any{p, 0.0, 1.0, 100.0, false, 1.0, 0.0, 64.0})
	}
	return Obj{"track": "Hats", "name": "hh", "midi": true, "notes": ns}
}

type side struct {
	live *fakeLive
	eng  *Engine
}

func pair(t *testing.T) (*side, *side, string) {
	root := filepath.Join(t.TempDir(), ".abcollab")
	tracks := []string{"Hats", "Bass"}
	mk := func(dev string) *side {
		os.MkdirAll(filepath.Join(root, "devices", dev), 0o755)
		fl := newFakeLive(t, tracks, map[string]Obj{"clip/0/s0": clip(60), "tempo": {"bpm": 120.0}})
		cl := Dial(fl.addr(), "tok")
		e := Start(cl, root, dev, func(d string) string { return "user-" + d }, func() []string { return tracks })
		t.Cleanup(func() { e.Close(); cl.Close() })
		return &side{fl, e}
	}
	a, b := mk("AAA"), mk("BBB")
	waitFor(t, "both matched", func() bool { return a.eng.Status().Matched && b.eng.Status().Matched })
	return a, b, root
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timeout: %s", what)
}

// drain: zbiera notatki silnika (do sprawdzania konfliktow).
func drain(e *Engine, kind string) func() *Note {
	var mu sync.Mutex
	var got *Note
	go func() {
		for n := range e.Notes() {
			if n.Kind == kind {
				mu.Lock()
				nn := n
				got = &nn
				mu.Unlock()
			}
		}
	}()
	return func() *Note { mu.Lock(); defer mu.Unlock(); return got }
}

func TestLiveChangeReachesOther(t *testing.T) {
	a, b, _ := pair(t)
	a.live.edit("clip/0/s0", clip(60, 64))
	waitFor(t, "B got A's notes", func() bool { return Hash(b.live.get("clip/0/s0")) == Hash(clip(60, 64)) })
	b.live.edit("tempo", Obj{"bpm": 128.0})
	waitFor(t, "A got B's tempo", func() bool { return Hash(a.live.get("tempo")) == Hash(Obj{"bpm": 128.0}) })
}

func TestLiveConcurrentEditAsks(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".abcollab")
	tracks := []string{"Hats"}
	os.MkdirAll(filepath.Join(root, "devices", "AAA"), 0o755)
	os.MkdirAll(filepath.Join(root, "devices", "BBB"), 0o755)
	la := newFakeLive(t, tracks, map[string]Obj{"clip/0/s0": clip(60)})
	lb := newFakeLive(t, tracks, map[string]Obj{"clip/0/s0": clip(60)})
	ca, cb := Dial(la.addr(), "tok"), Dial(lb.addr(), "tok")
	defer ca.Close()
	defer cb.Close()
	names := func(d string) string { return "user-" + d }
	ea := Start(ca, root, "AAA", names, func() []string { return tracks })
	eb := Start(cb, root, "BBB", names, func() []string { return tracks })
	defer ea.Close()
	defer eb.Close()
	aConf, bConf := drain(ea, "conflict"), drain(eb, "conflict")
	waitFor(t, "matched", func() bool { return ea.Status().Matched && eb.Status().Matched })

	// rownoczesne edycje: obie operacje maja te sama baze
	la.edit("clip/0/s0", clip(60, 62))
	lb.edit("clip/0/s0", clip(60, 67))
	waitFor(t, "conflicts on both sides", func() bool { return aConf() != nil && bConf() != nil })
	if n := aConf(); Hash(n.Mine) != Hash(clip(60, 62)) || Hash(n.Theirs) != Hash(clip(60, 67)) || n.From != "user-BBB" {
		t.Fatalf("A's conflict: %+v", n)
	}
	// nic nie zostalo nadpisane bez pytania
	if Hash(la.get("clip/0/s0")) != Hash(clip(60, 62)) || Hash(lb.get("clip/0/s0")) != Hash(clip(60, 67)) {
		t.Fatal("a concurrent edit overwrote the other one")
	}
	// A wybiera wersje B; B dostaje rozstrzygniecie i zamyka konflikt
	ea.Resolve("clip/0/s0", false)
	waitFor(t, "A applied B's version", func() bool { return Hash(la.get("clip/0/s0")) == Hash(clip(60, 67)) })
	waitFor(t, "B stays on its version", func() bool { return Hash(lb.get("clip/0/s0")) == Hash(clip(60, 67)) })
	// po rozstrzygnieciu dalsze zmiany plyna normalnie
	lb.edit("clip/0/s0", clip(60, 67, 70))
	waitFor(t, "sync after resolve", func() bool { return Hash(la.get("clip/0/s0")) == Hash(clip(60, 67, 70)) })
}

func TestLiveBothResolveDifferently(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".abcollab")
	tracks := []string{"Hats"}
	os.MkdirAll(filepath.Join(root, "devices", "AAA"), 0o755)
	os.MkdirAll(filepath.Join(root, "devices", "BBB"), 0o755)
	la := newFakeLive(t, tracks, map[string]Obj{"clip/0/s0": clip(60)})
	lb := newFakeLive(t, tracks, map[string]Obj{"clip/0/s0": clip(60)})
	ca, cb := Dial(la.addr(), "tok"), Dial(lb.addr(), "tok")
	defer ca.Close()
	defer cb.Close()
	names := func(d string) string { return d }
	ea := Start(ca, root, "AAA", names, func() []string { return tracks })
	eb := Start(cb, root, "BBB", names, func() []string { return tracks })
	defer ea.Close()
	defer eb.Close()
	aConf, bConf := drain(ea, "conflict"), drain(eb, "conflict")
	waitFor(t, "matched", func() bool { return ea.Status().Matched && eb.Status().Matched })
	la.edit("clip/0/s0", clip(1))
	lb.edit("clip/0/s0", clip(2))
	waitFor(t, "conflicts", func() bool { return aConf() != nil && bConf() != nil })
	// kazdy wybiera swoja - wygrywa mniejsze ID (AAA)
	ea.Resolve("clip/0/s0", true)
	eb.Resolve("clip/0/s0", true)
	waitFor(t, "both on AAA's version", func() bool {
		return Hash(la.get("clip/0/s0")) == Hash(clip(1)) && Hash(lb.get("clip/0/s0")) == Hash(clip(1))
	})
}

func TestLiveDifferentSetOpen(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".abcollab")
	os.MkdirAll(filepath.Join(root, "devices", "AAA"), 0o755)
	fl := newFakeLive(t, []string{"Some", "Other", "Set"}, map[string]Obj{})
	cl := Dial(fl.addr(), "tok")
	defer cl.Close()
	e := Start(cl, root, "AAA", func(d string) string { return d }, func() []string { return []string{"Hats"} })
	defer e.Close()
	waitFor(t, "status", func() bool { return e.Status().Connected })
	if st := e.Status(); st.Matched || st.Detail == "" {
		t.Fatalf("%+v", st)
	}
	fl.edit("clip/0/s0", clip(60))
	time.Sleep(200 * time.Millisecond)
	if ents, _ := os.ReadDir(filepath.Join(root, "devices", "AAA", "live")); len(ents) != 0 {
		t.Fatal("changes from a different set were sent")
	}
}

func TestLabel(t *testing.T) {
	if l := Label("clip/0/s2", Obj{"track": "Hats", "name": "hh"}); l != `Hats slot 3 clip "hh"` {
		t.Error(l)
	}
	if l := Label("mixer/1", Obj{"track": "Bass"}); l != "Bass mixer" {
		t.Error(l)
	}
	if l := Label("tempo", Obj{"bpm": 128.0}); l != "tempo (128 BPM)" {
		t.Error(l)
	}
}

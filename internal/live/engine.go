package live

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// --- operacje miedzy uczestnikami -----------------------------------------------------
//
// Zmiana w Live = operacja zapisana jako plik w moim katalogu sesji
// (.abcollab/devices/<ja>/live/<seq>.json). Syncthing dostarcza ja innym, ich
// silnik nanosi ja w ich Live przez skrypt. Kazda operacja niesie odcisk stanu,
// na ktorym zostala zrobiona (Base): jesli u odbiorcy ten klucz zmienil sie w
// miedzyczasie inaczej, to jednoczesna edycja - konflikt, nie nadpisanie.

type Op struct {
	Seq     int      `json:"seq"`
	Time    int64    `json:"time"` // unix ms
	Key     string   `json:"key"`
	Base    string   `json:"base"`
	Obj     Obj      `json:"obj"`
	Resolve []string `json:"resolve,omitempty"` // rozstrzygniecie konfliktu: para odciskow
}

// Note: cos do pokazania w interfejsie.
type Note struct {
	Kind string // log, conflict, structure, status, local, reload
	Text string
	// conflict
	Key, Label   string
	Mine, Theirs Obj
	From         string
}

// Status: stan polaczenia z Live'em.
type Status struct {
	Connected bool   // skrypt odpowiada
	Matched   bool   // w Live jest otwarty set tej sesji
	Detail    string // np. "Live has a different set open"
}

// Engine: synchronizacja na zywo jednej sesji.
type Engine struct {
	cl      *Client
	root    string // .abcollab sesji
	me      string // ID urzadzenia (store)
	session string // ID sesji (znacznik w secie Live'a)
	names   func(dev string) string
	tracks  func() []string // sciezki seta sesji (sprawdzenie, czy w Live jest ten set)
	notes   chan Note
	cmds    chan func()
	stop    chan struct{}
	closeMu sync.Once

	// stan (tylko w goroutine run)
	status   Status
	local    map[string]string // odcisk stanu w moim Live
	synced   map[string]string // odcisk ostatniego stanu uzgodnionego z innymi
	cur      map[string]Obj    // aktualny stan w moim Live
	paused   map[string]*conflict
	chosen   map[string][]string // moje rozstrzygniecia: para odciskow konfliktu
	seq      int
	seen     map[string]int // ostatnio przeczytana operacja kazdego urzadzenia
	pending  map[int]pendingApply
	statusMu sync.Mutex
	pub      Status
}

type conflict struct {
	mine, theirs Obj
	mineAt       string // odcisk mojej wersji w chwili konfliktu
	from         string
	pair         []string
	resolved     string // odcisk mojego wyboru (czekamy na druga strone)
}

type pendingApply struct {
	key  string
	obj  Obj
	from string
}

// Start: silnik dla sesji w root (.abcollab). me - moje ID w store, session -
// ID sesji (zapisywany w secie Live'a, zeby rozpoznac go niezaleznie od sciezek).
func Start(cl *Client, root, me, session string, names func(string) string, tracks func() []string) *Engine {
	e := &Engine{cl: cl, root: root, me: me, session: session, names: names, tracks: tracks,
		notes: make(chan Note, 256), cmds: make(chan func(), 16), stop: make(chan struct{}),
		local: map[string]string{}, synced: map[string]string{}, cur: map[string]Obj{},
		paused: map[string]*conflict{}, chosen: map[string][]string{}, seen: map[string]int{}, pending: map[int]pendingApply{}}
	e.seq = e.lastSeq(e.me)
	for _, d := range e.peers() {
		e.seen[d] = e.lastSeq(d) // starsze operacje sa juz w wersjach
	}
	e.pub = Status{Detail: notConnected}
	go e.run()
	return e
}

const notConnected = "Live not connected - in Live: Settings > Link, Tempo & MIDI > Control Surface: AbletonCollab"

// Recheck: sprawdz ponownie, czy w Live jest set tej sesji (np. po zapisie).
func (e *Engine) Recheck() {
	select {
	case e.cmds <- func() {
		if e.status.Connected {
			e.cl.Send(map[string]any{"op": "state"})
		}
	}:
	case <-e.stop:
	}
}

func (e *Engine) Notes() <-chan Note { return e.notes }

func (e *Engine) Status() Status {
	e.statusMu.Lock()
	defer e.statusMu.Unlock()
	return e.pub
}

func (e *Engine) Close() {
	e.closeMu.Do(func() { close(e.stop) })
}

// Resolve: wybor w konflikcie na kluczu key (mine - zostaje moja wersja).
func (e *Engine) Resolve(key string, mine bool) {
	select {
	case e.cmds <- func() { e.resolve(key, mine) }:
	case <-e.stop:
	}
}

func (e *Engine) note(n Note) {
	select {
	case e.notes <- n:
	default: // interfejs nie nadaza - log moze zgubic wpis, stan nie
	}
}

func (e *Engine) setStatus(s Status) {
	e.status = s
	e.statusMu.Lock()
	e.pub = s
	e.statusMu.Unlock()
	e.note(Note{Kind: "status"})
}

func (e *Engine) run() {
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-e.stop:
			return
		case f := <-e.cmds:
			f()
		case m := <-e.cl.Msgs():
			e.onMsg(m)
		case <-tick.C:
			if e.status.Matched {
				e.readPeers()
			}
		}
	}
}

func (e *Engine) onMsg(m Msg) {
	switch m.T {
	case "up":
		e.setStatus(Status{Connected: true, Detail: "checking the set open in Live..."})
		e.cl.Send(map[string]any{"op": "state"})
	case "down":
		e.setStatus(Status{Detail: notConnected})
	case "error":
		e.setStatus(Status{Detail: "Live: " + m.Err})
	case "state":
		// Live uruchamia skrypt od nowa przy kazdym otwarciu setu, wiec set
		// sprawdzamy raz na polaczenie - dodanie sciezki niczego nie blokuje.
		if !e.status.Matched {
			marked := e.session != "" && m.Session == e.session
			if !marked && !sameTracks(e.tracks(), m.Tracks) {
				e.setStatus(Status{Connected: true, Detail: "Live has a different set open - open this session's set"})
				return
			}
			if !marked && e.session != "" {
				e.cl.Send(map[string]any{"op": "mark", "session": e.session})
			}
		}
		for k, o := range m.Items {
			h := Hash(o)
			e.cur[k], e.local[k] = o, h
			if _, ok := e.synced[k]; !ok {
				e.synced[k] = h
			}
		}
		if !e.status.Matched {
			e.setStatus(Status{Connected: true, Matched: true})
		}
	case "structure":
		if e.status.Matched {
			e.note(Note{Kind: "structure"})
			e.cl.Send(map[string]any{"op": "state"}) // nowe klucze (np. nowy klip)
		}
	case "change":
		if !e.status.Matched {
			return
		}
		e.onLocal(m.Key, m.Obj)
	case "applied":
		p, ok := e.pending[m.ID]
		if !ok {
			return
		}
		delete(e.pending, m.ID)
		if !m.OK {
			e.note(Note{Kind: "reload", Text: fmt.Sprintf("%s's change to %s needs a reload: %s", p.from, Label(p.key, p.obj), m.Err)})
			return
		}
		h := Hash(p.obj)
		e.cur[p.key], e.local[p.key], e.synced[p.key] = p.obj, h, h
		e.note(Note{Kind: "log", Text: fmt.Sprintf("%s changed %s", p.from, Label(p.key, p.obj))})
	}
}

// onLocal: zmiana w moim Live - wysylamy (chyba ze klucz czeka na rozstrzygniecie).
func (e *Engine) onLocal(key string, obj Obj) {
	h := Hash(obj)
	e.cur[key], e.local[key] = obj, h
	if c := e.paused[key]; c != nil {
		c.mine = obj
		return
	}
	if h == e.synced[key] {
		return
	}
	if err := e.write(Op{Key: key, Base: e.synced[key], Obj: obj}); err != nil {
		e.note(Note{Kind: "log", Text: "can't send a change: " + err.Error()})
		return
	}
	e.synced[key] = h
	e.note(Note{Kind: "local", Text: Label(key, obj)})
}

// onRemote: operacja od innego uczestnika.
func (e *Engine) onRemote(dev string, op Op) {
	who := e.names(dev)
	h := Hash(op.Obj)
	if len(op.Resolve) == 2 {
		e.onResolve(dev, who, op)
		return
	}
	if c := e.paused[op.Key]; c != nil {
		c.theirs, c.from = op.Obj, who // nowsza wersja tej samej osoby
		return
	}
	switch {
	case h == e.local[op.Key]:
		e.synced[op.Key] = h // juz to mam
	case e.local[op.Key] == e.synced[op.Key] && (op.Base == e.synced[op.Key] || op.Base == ""):
		e.apply(op.Key, op.Obj, who)
	default:
		// oba Live'y zmienily ten klucz od ostatniego uzgodnionego stanu
		mine := e.cur[op.Key]
		pair := []string{e.local[op.Key], h}
		sort.Strings(pair)
		e.paused[op.Key] = &conflict{mine: mine, mineAt: e.local[op.Key], theirs: op.Obj, from: who, pair: pair}
		e.note(Note{Kind: "conflict", Key: op.Key, Label: Label(op.Key, op.Obj), Mine: mine, Theirs: op.Obj, From: who})
	}
}

func (e *Engine) apply(key string, obj Obj, who string) {
	id, ok := e.cl.Send(map[string]any{"op": "apply", "key": key, "obj": obj})
	if ok {
		e.pending[id] = pendingApply{key: key, obj: obj, from: who}
	}
}

// resolve: moj wybor. Wysylamy go jako operacje z para odciskow - druga
// strona, jesli wybrala inaczej, ustapi urzadzeniu o mniejszym ID.
func (e *Engine) resolve(key string, mine bool) {
	c := e.paused[key]
	if c == nil {
		return
	}
	pick := c.theirs
	if mine {
		pick = c.mine
	}
	h := Hash(pick)
	c.resolved = h
	if err := e.write(Op{Key: key, Base: e.synced[key], Obj: pick, Resolve: c.pair}); err != nil {
		e.note(Note{Kind: "log", Text: "can't send the choice: " + err.Error()})
		return
	}
	if !mine {
		e.apply(key, pick, c.from)
	}
	e.synced[key] = h
	e.chosen[key] = c.pair
	delete(e.paused, key)
	e.note(Note{Kind: "log", Text: "Kept " + map[bool]string{true: "your", false: c.from + "'s"}[mine] + " version of " + Label(key, pick)})
}

func (e *Engine) onResolve(dev, who string, op Op) {
	h := Hash(op.Obj)
	if c := e.paused[op.Key]; c != nil {
		// rozstrzygnal ktos inny, zanim ja wybralem - przyjmujemy
		delete(e.paused, op.Key)
		e.synced[op.Key] = h
		switch {
		case h == e.local[op.Key]:
		case h == c.mineAt:
			// zostala moja wersja, a ja pracowalem dalej - wysylamy dalsza prace
			e.onLocal(op.Key, e.cur[op.Key])
		default:
			e.apply(op.Key, op.Obj, who)
		}
		e.note(Note{Kind: "log", Text: who + " resolved the conflict on " + Label(op.Key, op.Obj)})
		e.note(Note{Kind: "resolved", Key: op.Key})
		return
	}
	if h == e.local[op.Key] {
		e.synced[op.Key] = h
		return
	}
	// obaj wybralismy w tym samym konflikcie: wygrywa mniejsze ID urzadzenia
	if pair := e.chosen[op.Key]; samePair(pair, op.Resolve) && dev > e.me {
		return
	}
	e.apply(op.Key, op.Obj, who)
	e.synced[op.Key] = h
}

func samePair(a, b []string) bool { return len(a) == 2 && len(b) == 2 && a[0] == b[0] && a[1] == b[1] }

// --- pliki operacji ------------------------------------------------------------------------

func (e *Engine) opsDir(dev string) string { return filepath.Join(e.root, "devices", dev, "live") }

func (e *Engine) write(op Op) error {
	e.seq++
	op.Seq, op.Time = e.seq, time.Now().UnixMilli()
	dir := e.opsDir(e.me)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, _ := json.Marshal(op)
	name := filepath.Join(dir, fmt.Sprintf("%012d.json", op.Seq))
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, name); err != nil {
		return err
	}
	// stare operacje sa juz niepotrzebne (zmiany trafiaja tez do wersji)
	if op.Seq > 300 {
		os.Remove(filepath.Join(dir, fmt.Sprintf("%012d.json", op.Seq-300)))
	}
	return nil
}

func (e *Engine) peers() []string {
	ents, _ := os.ReadDir(filepath.Join(e.root, "devices"))
	var out []string
	for _, d := range ents {
		if d.IsDir() && d.Name() != e.me {
			out = append(out, d.Name())
		}
	}
	return out
}

func (e *Engine) seqs(dev string) []int {
	ents, _ := os.ReadDir(e.opsDir(dev))
	var out []int
	for _, f := range ents {
		n, ok := strings.CutSuffix(f.Name(), ".json")
		if !ok {
			continue
		}
		if v, err := strconv.Atoi(n); err == nil {
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

func (e *Engine) lastSeq(dev string) int {
	s := e.seqs(dev)
	if len(s) == 0 {
		return 0
	}
	return s[len(s)-1]
}

func (e *Engine) readPeers() {
	for _, dev := range e.peers() {
		for _, n := range e.seqs(dev) {
			if n <= e.seen[dev] {
				continue
			}
			data, err := os.ReadFile(filepath.Join(e.opsDir(dev), fmt.Sprintf("%012d.json", n)))
			if err != nil {
				break // jeszcze w drodze - sprobujemy za chwile, po kolei
			}
			var op Op
			if json.Unmarshal(data, &op) != nil {
				break
			}
			e.seen[dev] = n
			e.onRemote(dev, op)
		}
	}
}

// --- opisy ---------------------------------------------------------------------------------

func sameTracks(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Label: klucz po ludzku, np. `Hats clip "hh"`, `Bass mixer`, `tempo`.
func Label(key string, o Obj) string {
	if key == "tempo" {
		if v, ok := o["bpm"].(float64); ok {
			return fmt.Sprintf("tempo (%g BPM)", v)
		}
		return "tempo"
	}
	track, _ := o["track"].(string)
	parts := strings.Split(key, "/")
	if parts[0] == "mixer" {
		return track + " mixer"
	}
	where := ""
	if len(parts) == 3 && strings.HasPrefix(parts[2], "s") {
		n, _ := strconv.Atoi(parts[2][1:])
		where = fmt.Sprintf(" slot %d", n+1)
	} else if len(parts) == 3 {
		where = " arrangement"
	}
	if name, _ := o["name"].(string); name != "" {
		return fmt.Sprintf("%s%s clip %q", track, where, name)
	}
	return track + where + " clip"
}

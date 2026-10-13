package live

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"sync"
	"time"
)

const Addr = "127.0.0.1:9879"

// Obj: stan jednego klucza (klip, mikser sciezki, tempo) - JSON ze skryptu.
type Obj map[string]any

// Hash: odcisk stanu (kanoniczny JSON - mapy w Go maja posortowane klucze).
func Hash(o Obj) string {
	if o == nil {
		return ""
	}
	data, _ := json.Marshal(o)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// Msg: wiadomosc od skryptu.
type Msg struct {
	T       string         `json:"t"` // hello, state, change, structure, applied, pong, error
	ID      int            `json:"id"`
	Key     string         `json:"key"`
	Obj     Obj            `json:"obj"`
	Tracks  []string       `json:"tracks"`
	Items   map[string]Obj `json:"items"`
	OK      bool           `json:"ok"`
	Err     string         `json:"err"`
	Session string         `json:"session"` // znacznik sesji zapisany w secie
	V       int            `json:"v"`       // wersja skryptu (hello)
}

// Client: polaczenie ze skryptem w Live (laczy sie ponownie samo, np. gdy
// Live startuje pozniej niz abcollab). Zdarzenia - kanal Msgs; T "down" i
// "up" oznaczaja zerwanie i nawiazanie polaczenia.
type Client struct {
	addr, token string
	msgs        chan Msg
	mu          sync.Mutex
	conn        net.Conn
	next        int
	stop        chan struct{}
}

func Dial(addr, token string) *Client {
	c := &Client{addr: addr, token: token, msgs: make(chan Msg, 256), stop: make(chan struct{})}
	go c.loop()
	return c
}

func (c *Client) Msgs() <-chan Msg { return c.msgs }

func (c *Client) Close() {
	select {
	case <-c.stop:
	default:
		close(c.stop)
	}
	c.mu.Lock()
	if c.conn != nil {
		c.conn.Close()
	}
	c.mu.Unlock()
}

func (c *Client) emit(m Msg) {
	select {
	case c.msgs <- m:
	case <-c.stop:
	}
}

func (c *Client) loop() {
	for {
		select {
		case <-c.stop:
			return
		default:
		}
		conn, err := net.DialTimeout("tcp", c.addr, 2*time.Second)
		if err != nil {
			select {
			case <-c.stop:
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		c.mu.Lock()
		c.conn = conn
		c.mu.Unlock()
		c.Send(map[string]any{"op": "hello", "token": c.token})
		sc := bufio.NewScanner(conn)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			var m Msg
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				if m.T == "hello" {
					c.emit(Msg{T: "up", Tracks: m.Tracks, V: m.V})
				}
				c.emit(m)
			}
		}
		conn.Close()
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
		c.emit(Msg{T: "down"})
		select {
		case <-c.stop:
			return
		case <-time.After(time.Second):
		}
	}
}

// Send wysyla polecenie; zwraca jego id (odpowiedz ma ten sam id).
func (c *Client) Send(m map[string]any) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return 0, false
	}
	c.next++
	m["id"] = c.next
	data, _ := json.Marshal(m)
	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.conn.Write(append(data, '\n')); err != nil {
		c.conn.Close()
		return 0, false
	}
	return c.next, true
}

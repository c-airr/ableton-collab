package syncthing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client: REST API Syncthinga na 127.0.0.1 (klucz API w naglowku).
type Client struct {
	Base string // np. http://127.0.0.1:18401
	Key  string
	HTTP *http.Client
}

func newClient(base, key string) *Client {
	return &Client{Base: base, Key: key, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Client) do(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.Base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.Key)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("syncthing %s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(data))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Healthy: czy pod adresem odpowiada Syncthing z naszym kluczem.
func (c *Client) Healthy() bool {
	var st struct {
		MyID string `json:"myID"`
	}
	return c.do("GET", "/rest/system/status", nil, &st) == nil && st.MyID != ""
}

func (c *Client) MyID() (string, error) {
	var st struct {
		MyID string `json:"myID"`
	}
	if err := c.do("GET", "/rest/system/status", nil, &st); err != nil {
		return "", err
	}
	return st.MyID, nil
}

// --- konfiguracja ------------------------------------------------------------------

type DeviceConfig struct {
	DeviceID   string   `json:"deviceID"`
	Name       string   `json:"name,omitempty"`
	Addresses  []string `json:"addresses,omitempty"`
	Introducer bool     `json:"introducer"`
}

type FolderDevice struct {
	DeviceID string `json:"deviceID"`
}

type FolderConfig struct {
	ID      string         `json:"id"`
	Label   string         `json:"label"`
	Path    string         `json:"path"`
	Type    string         `json:"type,omitempty"`
	Devices []FolderDevice `json:"devices"`
	// FsWatcherDelayS: po ilu sekundach od zmiany na dysku Syncthing ja
	// wysyla (domyslnie 10 - za wolno na prace na zywo).
	FsWatcherDelayS int `json:"fsWatcherDelayS,omitempty"`
	// IgnorePerms: bez przenoszenia uprawnien. Windows oznacza folder projektu
	// Live'a (z Desktop.ini) jako "tylko do odczytu" i na Linuksie/macOS
	// powstawal folder bez prawa zapisu - set nie dal sie zapisac.
	IgnorePerms bool `json:"ignorePerms"`
}

func (c *Client) Devices() ([]DeviceConfig, error) {
	var out []DeviceConfig
	return out, c.do("GET", "/rest/config/devices", nil, &out)
}

// AddDevice dodaje albo zastepuje urzadzenie (POST na kolekcje = upsert).
func (c *Client) AddDevice(d DeviceConfig) error {
	if len(d.Addresses) == 0 {
		d.Addresses = []string{"dynamic"}
	}
	return c.do("POST", "/rest/config/devices", d, nil)
}

func (c *Client) PatchDevice(id string, patch map[string]any) error {
	return c.do("PATCH", "/rest/config/devices/"+url.PathEscape(id), patch, nil)
}

func (c *Client) Folder(id string) (*FolderConfig, error) {
	var f FolderConfig
	if err := c.do("GET", "/rest/config/folders/"+url.PathEscape(id), nil, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

func (c *Client) Folders() ([]FolderConfig, error) {
	var out []FolderConfig
	return out, c.do("GET", "/rest/config/folders", nil, &out)
}

func (c *Client) AddFolder(f FolderConfig) error {
	if f.Type == "" {
		f.Type = "sendreceive"
	}
	if f.FsWatcherDelayS == 0 {
		f.FsWatcherDelayS = 1
	}
	f.IgnorePerms = true
	return c.do("POST", "/rest/config/folders", f, nil)
}

func (c *Client) PatchFolder(id string, patch map[string]any) error {
	return c.do("PATCH", "/rest/config/folders/"+url.PathEscape(id), patch, nil)
}

func (c *Client) SetFolderDevices(id string, devs []FolderDevice) error {
	return c.do("PATCH", "/rest/config/folders/"+url.PathEscape(id), map[string]any{"devices": devs}, nil)
}

func (c *Client) PatchOptions(patch map[string]any) error {
	return c.do("PATCH", "/rest/config/options", patch, nil)
}

func (c *Client) RestartRequired() (bool, error) {
	var r struct {
		RequiresRestart bool `json:"requiresRestart"`
	}
	return r.RequiresRestart, c.do("GET", "/rest/config/restart-required", nil, &r)
}

// IgnoreDevice: odrzucone urzadzenie trafia do remoteIgnoredDevices, zeby nie
// pojawialo sie ponownie przy kazdej probie polaczenia. Ta lista jest dostepna
// tylko przez pelna konfiguracje (GET/PUT /rest/config).
func (c *Client) IgnoreDevice(id, name, address string) error {
	var cfg map[string]any
	if err := c.do("GET", "/rest/config", nil, &cfg); err != nil {
		return err
	}
	list, _ := cfg["remoteIgnoredDevices"].([]any)
	list = append(list, map[string]any{
		"deviceID": id, "name": name, "address": address,
		"time": time.Now().UTC().Format(time.RFC3339),
	})
	cfg["remoteIgnoredDevices"] = list
	return c.do("PUT", "/rest/config", cfg, nil)
}

// --- stan --------------------------------------------------------------------------

type Pending struct {
	Time    time.Time `json:"time"`
	Name    string    `json:"name"`
	Address string    `json:"address"`
}

func (c *Client) PendingDevices() (map[string]Pending, error) {
	out := map[string]Pending{}
	return out, c.do("GET", "/rest/cluster/pending/devices", nil, &out)
}

func (c *Client) DismissPendingDevice(id string) error {
	return c.do("DELETE", "/rest/cluster/pending/devices?device="+url.QueryEscape(id), nil, nil)
}

type Connection struct {
	Connected bool   `json:"connected"`
	Type      string `json:"type"` // tcp-client, tcp-server, quic-client, quic-server, relay-client, relay-server
	Address   string `json:"address"`
}

func (c *Client) Connections() (map[string]Connection, error) {
	var r struct {
		Connections map[string]Connection `json:"connections"`
	}
	return r.Connections, c.do("GET", "/rest/system/connections", nil, &r)
}

type FolderStatus struct {
	State       string `json:"state"`
	GlobalFiles int    `json:"globalFiles"`
	NeedFiles   int    `json:"needFiles"`
	NeedBytes   int64  `json:"needBytes"`
	Error       string `json:"error"`
}

func (c *Client) FolderStatus(id string) (*FolderStatus, error) {
	var st FolderStatus
	return &st, c.do("GET", "/rest/db/status?folder="+url.QueryEscape(id), nil, &st)
}

func (c *Client) Shutdown() error { return c.do("POST", "/rest/system/shutdown", nil, nil) }

func (c *Client) RemoveDevice(id string) error {
	return c.do("DELETE", "/rest/config/devices/"+url.PathEscape(id), nil, nil)
}

func (c *Client) RemoveFolder(id string) error {
	return c.do("DELETE", "/rest/config/folders/"+url.PathEscape(id), nil, nil)
}

type RawEvent struct {
	ID   int            `json:"id"`
	Type string         `json:"type"`
	Time time.Time      `json:"time"`
	Data map[string]any `json:"data"`
}

// RawEvents: long-poll /rest/events (czeka do timeout na nowe zdarzenia).
func (c *Client) RawEvents(since int, timeout time.Duration) ([]RawEvent, error) {
	q := fmt.Sprintf("/rest/events?since=%d&timeout=%d&events=ItemFinished,LocalIndexUpdated,DeviceConnected,DeviceDisconnected,PendingDevicesChanged",
		since, int(timeout.Seconds()))
	hc := *c.HTTP
	hc.Timeout = timeout + 10*time.Second
	cl := *c
	cl.HTTP = &hc
	var out []RawEvent
	return out, cl.do("GET", q, nil, &out)
}

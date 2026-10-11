// Package als czyta, porownuje i scala projekty Ableton Live (.als).
//
// Plik .als to gzip z XML-em. Live zapisuje go w stalym formacie: CRLF,
// wciecia tabami, jeden element na linie, puste elementy jako `<X ... />`.
// Parser trzyma drzewo w pamieci, a writer odtwarza ten format bajt w bajt
// (sprawdza to test roundtrip na prawdziwych plikach).
package als

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"
)

type Attr struct {
	Name, Value string
}

type Node struct {
	Name     string
	Attrs    []Attr
	Children []*Node
	// Text to surowa zawartosc tekstowa (np. heksowy blob stanu wtyczki).
	// Pusta dla zwyklych elementow - same biale znaki sa odtwarzane z wciec.
	Text string
}

type Doc struct {
	ProcInst string // np. `xml version="1.0" encoding="UTF-8"`
	Root     *Node
}

func (n *Node) Attr(name string) (string, bool) {
	for _, a := range n.Attrs {
		if a.Name == name {
			return a.Value, true
		}
	}
	return "", false
}

func (n *Node) SetAttr(name, value string) {
	for i := range n.Attrs {
		if n.Attrs[i].Name == name {
			n.Attrs[i].Value = value
			return
		}
	}
	n.Attrs = append(n.Attrs, Attr{name, value})
}

// Child zwraca pierwsze dziecko o danej nazwie.
func (n *Node) Child(name string) *Node {
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Find idzie sciezka nazw, np. Find("Name", "EffectiveName").
func (n *Node) Find(path ...string) *Node {
	cur := n
	for _, p := range path {
		if cur = cur.Child(p); cur == nil {
			return nil
		}
	}
	return cur
}

// Value zwraca atrybut Value elementu pod sciezka.
func (n *Node) Value(path ...string) string {
	if c := n.Find(path...); c != nil {
		v, _ := c.Attr("Value")
		return v
	}
	return ""
}

func (d *Doc) Clone() *Doc { return &Doc{ProcInst: d.ProcInst, Root: d.Root.Clone()} }

func (n *Node) Clone() *Node {
	c := &Node{Name: n.Name, Text: n.Text}
	c.Attrs = append([]Attr(nil), n.Attrs...)
	c.Children = make([]*Node, len(n.Children))
	for i, ch := range n.Children {
		c.Children[i] = ch.Clone()
	}
	return c
}

// Walk odwiedza wezel i wszystkich potomkow (pre-order).
func (n *Node) Walk(fn func(*Node)) {
	fn(n)
	for _, c := range n.Children {
		c.Walk(fn)
	}
}

func ReadFile(path string) (*Doc, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	d, err := Read(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

// Read przyjmuje gzip (normalny .als) albo goly XML.
func Read(r io.Reader) (*Doc, error) {
	br := bufio.NewReader(r)
	magic, _ := br.Peek(2)
	var src io.Reader = br
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		src = gz
	}
	return ParseXML(src)
}

func ParseXML(r io.Reader) (*Doc, error) {
	dec := xml.NewDecoder(r)
	doc := &Doc{}
	var stack []*Node
	for {
		tok, err := dec.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.ProcInst:
			doc.ProcInst = t.Target + " " + string(t.Inst)
		case xml.StartElement:
			n := &Node{Name: t.Name.Local}
			for _, a := range t.Attr {
				n.Attrs = append(n.Attrs, Attr{a.Name.Local, a.Value})
			}
			if len(stack) == 0 {
				if doc.Root != nil {
					return nil, fmt.Errorf("more than one root element")
				}
				doc.Root = n
			} else {
				p := stack[len(stack)-1]
				p.Children = append(p.Children, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, fmt.Errorf("unmatched </%s>", t.Name.Local)
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(t)
			}
		}
	}
	if doc.Root == nil {
		return nil, fmt.Errorf("no root element")
	}
	// tekst z samych bialych znakow to tylko formatowanie
	doc.Root.Walk(func(n *Node) {
		if strings.TrimSpace(n.Text) == "" {
			n.Text = ""
		}
	})
	return doc, nil
}

const nl = "\r\n"

// WriteXML zapisuje drzewo w formacie Live'a (CRLF, taby).
func (d *Doc) WriteXML(w io.Writer) error {
	bw := bufio.NewWriter(w)
	if d.ProcInst != "" {
		bw.WriteString("<?" + d.ProcInst + "?>" + nl)
	}
	writeNode(bw, d.Root, 0)
	return bw.Flush()
}

func writeNode(w *bufio.Writer, n *Node, depth int) {
	ind := strings.Repeat("\t", depth)
	w.WriteString(ind + "<" + n.Name)
	for _, a := range n.Attrs {
		w.WriteString(" " + a.Name + "=" + quoteAttr(a.Value))
	}
	switch {
	case len(n.Children) == 0 && n.Text == "":
		w.WriteString(" />" + nl)
	case n.Text != "":
		// encoding/xml zamienia CRLF na LF, wiec przywracamy CRLF
		w.WriteString(">" + strings.ReplaceAll(escapeText(n.Text), "\n", nl) + "</" + n.Name + ">" + nl)
	default:
		w.WriteString(">" + nl)
		for _, c := range n.Children {
			writeNode(w, c, depth+1)
		}
		w.WriteString(ind + "</" + n.Name + ">" + nl)
	}
}

var textEsc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
var sqEsc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "'", "&apos;")

// Live pisze atrybut w apostrofach, gdy wartosc zawiera cudzyslow (np. JSON
// w ViewData) - inaczej w cudzyslowach.
func quoteAttr(s string) string {
	if strings.Contains(s, `"`) {
		return "'" + sqEsc.Replace(s) + "'"
	}
	return `"` + textEsc.Replace(s) + `"`
}

func escapeText(s string) string { return textEsc.Replace(s) }

func (d *Doc) XMLBytes() []byte {
	var b bytes.Buffer
	d.WriteXML(&b)
	return b.Bytes()
}

// Encode zwraca plik .als (gzip z XML-em). Naglowek gzip bez czasu
// modyfikacji, wiec ten sam dokument daje zawsze te same bajty.
func (d *Doc) Encode() ([]byte, error) {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	if err := d.WriteXML(gz); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// WriteFile zapisuje .als atomowo: najpierw plik tymczasowy, potem rename.
func (d *Doc) WriteFile(path string) error {
	data, err := d.Encode()
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, data)
}

// WriteFileAtomic: zapis do pliku tymczasowego obok i rename - czytajacy
// (Live, Syncthing) nigdy nie zobaczy polowy pliku.
func WriteFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

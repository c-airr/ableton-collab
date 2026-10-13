package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/c-airr/ableton-collab/als"
)

// --- Znacznik wersji w secie -------------------------------------------------------
//
// Kazdy plik roboczy, ktory zapisujemy (pobranie, scalenie, Reload), dostaje
// w danych seta (LiveSet/ViewData, te same co Song.set_data) identyfikator
// wersji, ktora zawiera. Live trzyma te dane w pamieci i zapisuje je z
// powrotem, wiec zapis z Live'a mowi, z ktorej wersji pochodzi jego tresc.
// Gdy Live nie przeladowal podmienionego pliku (Reload bez ponownego
// otwarcia), zapisze stara tresc ze starym znacznikiem - a nie z nowym
// rodzicem, ktory "cofnalby" zmiany innych.
//
// lineage: znacznik -> ostatnia moja wersja zapisana z tresci o tym
// znaczniku (kolejne zapisy z Live'a ida po sobie, a znacznik w pamieci
// Live'a zostaje ten z otwarcia).

const baseKey = "abcollab.base"

func setViewData(d *als.Doc) *als.Node {
	ls := d.Root.Child("LiveSet")
	if ls == nil {
		return nil
	}
	return ls.Child("ViewData")
}

// docBase: znacznik wersji w secie ("" - brak, np. set sprzed znacznikow).
func docBase(d *als.Doc) string {
	vd := setViewData(d)
	if vd == nil {
		return ""
	}
	raw, _ := vd.Attr("Value")
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return ""
	}
	id, _ := m[baseKey].(string)
	if !validID(id) {
		return ""
	}
	return id
}

// writeWorkingVersion: plik roboczy = wersja id (data - jej bajty), ze
// znacznikiem. Live, ktory go wczyta, zapisze tresc pochodzaca z id.
func (p *Project) writeWorkingVersion(data []byte, id string) error {
	marked, err := withBase(data, id)
	if err != nil {
		return err
	}
	if err := writableDir(filepath.Dir(p.WorkingPath())); err != nil {
		return err
	}
	if marked == nil {
		// set bez ViewData (starszy Live) - bez znacznika; set moze lezec w
		// podfolderze, ktory u nas jeszcze nie istnieje (writableDir wyzej)
		if err := writeFileAtomic(p.WorkingPath(), data); err != nil {
			return err
		}
		return p.setLineage("", id)
	}
	// Live z setem sprzed znacznikow (otwartym, zanim abcollab je pisal)
	// zapisze go bez znacznika - jego linia to to, co mial do tej pory
	if _, ok := p.lineage()[""]; !ok {
		if err := p.setLineage("", p.Loaded()); err != nil {
			return err
		}
	}
	if err := writeFileAtomic(p.WorkingPath(), marked); err != nil {
		return err
	}
	return p.setLineage(id, id)
}

// withBase: bajty seta ze znacznikiem wersji id; nil - set bez ViewData
// (starsze wersje Live'a), znacznika nie ma gdzie zapisac.
func withBase(data []byte, id string) ([]byte, error) {
	d, err := readDoc(data)
	if err != nil {
		return nil, err
	}
	vd := setViewData(d)
	if vd == nil {
		return nil, nil
	}
	raw, _ := vd.Attr("Value")
	m := map[string]json.RawMessage{}
	if json.Unmarshal([]byte(raw), &m) != nil {
		m = map[string]json.RawMessage{}
	}
	m[baseKey], _ = json.Marshal(id)
	out, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	vd.SetAttr("Value", string(out))
	return d.Encode()
}

func readDoc(data []byte) (*als.Doc, error) { return als.Read(bytes.NewReader(data)) }

func (p *Project) lineagePath() string { return filepath.Join(p.devDir(p.Me.ID), "lineage.json") }

func (p *Project) lineage() map[string]string {
	m := map[string]string{}
	data, err := os.ReadFile(p.lineagePath())
	if err == nil {
		json.Unmarshal(data, &m)
	}
	return m
}

// setLineage: tresc o znaczniku marker to teraz wersja id ("" - sety bez
// znacznika, sprzed tej wersji abcollab).
func (p *Project) setLineage(marker, id string) error {
	m := p.lineage()
	m[marker] = id
	// stare znaczniki (sety, ktorych Live juz dawno nie ma) - wystarczy kilka
	// ostatnich; kolejnosc nieistotna, wiec przy przepelnieniu czyscimy reszte
	if len(m) > 32 {
		m = map[string]string{marker: id}
	}
	if err := p.ensureMe(); err != nil {
		return err
	}
	data, _ := json.Marshal(m)
	return writeFileAtomic(p.lineagePath(), data)
}

// SameSet: ta sama tresc seta, bez wzgledu na znacznik wersji (plik roboczy
// po pobraniu = obiekt wersji + znacznik).
func SameSet(a, b []byte) bool {
	norm := func(data []byte) []byte {
		d, err := readDoc(data)
		if err != nil {
			return data
		}
		if vd := setViewData(d); vd != nil {
			raw, _ := vd.Attr("Value")
			m := map[string]json.RawMessage{}
			json.Unmarshal([]byte(raw), &m)
			delete(m, baseKey)
			out, _ := json.Marshal(m)
			vd.SetAttr("Value", string(out))
		}
		return d.XMLBytes()
	}
	return bytes.Equal(norm(a), norm(b))
}

package als

import (
	"crypto/sha1"
	"fmt"
	"strconv"
	"strings"
)

// --- Tozsamosc dzieci --------------------------------------------------------
//
// Zeby porownac drzewa strukturalnie, kazde dziecko dostaje klucz unikalny
// wsrod rodzenstwa. Reguly wynikaja z obserwacji prawdziwych plikow:
//   - MidiNoteEvent ma NoteId (staly miedzy zapisami, nadawany z NoteIdGenerator),
//   - KeyTrack ma Id, ale to tylko indeks - przy dodaniu nowej wysokosci
//     Live wstawia KeyTrack Id="1" przed istniejacym; stala tozsamosc to MidiKey,
//   - NamedRemoteableKeyMidi i FileRef maja Id przenumerowywane przy zapisie,
//   - reszta elementow z unikalnym Id (sciezki, ClipSlot, Scene, ...) jest
//     kluczowana po Id, a elementy bez Id - po pozycji wsrod rodzenstwa
//     o tej samej nazwie.

func childKeys(n *Node) []string {
	keys := make([]string, len(n.Children))
	idCount := map[string]int{}
	for _, c := range n.Children {
		if id, ok := c.Attr("Id"); ok {
			idCount[c.Name+"#"+id]++
		}
	}
	pos := map[string]int{}
	seen := map[string]int{}
	for i, c := range n.Children {
		k := ""
		switch c.Name {
		case "MidiNoteEvent":
			if id, ok := c.Attr("NoteId"); ok {
				k = "MidiNoteEvent#" + id
			}
		case "KeyTrack":
			if c.Find("MidiKey") != nil {
				k = "KeyTrack@" + c.Value("MidiKey")
			}
		case "NamedRemoteableKeyMidi":
			if nm, ok := c.Attr("Name"); ok {
				k = "NamedRemoteableKeyMidi@" + nm
			}
		case "FileRef":
			// Id przenumerowywane przy zapisie
		default:
			if id, ok := c.Attr("Id"); ok && idCount[c.Name+"#"+id] == 1 {
				k = c.Name + "#" + id
			}
		}
		if k == "" {
			k = c.Name + "[" + strconv.Itoa(pos[c.Name]) + "]"
			pos[c.Name]++
		}
		if seen[k] > 0 {
			k += "~" + strconv.Itoa(seen[k])
		}
		seen[k]++
		keys[i] = k
	}
	return keys
}

// --- Szum ----------------------------------------------------------------------
//
// Elementy i atrybuty, ktore zmieniaja sie przy samym otwarciu/zapisie albo
// sa stanem widoku (zaznaczenie, scroll, zoom, pozycja odtwarzania). Nie biora
// udzialu w diffie ani w wykrywaniu konfliktow; przy merge'u wygrywa "ours".
// Kazda pozycja jest zaobserwowana w testdata (patrz RAPORT.md).

var noiseElems = map[string]bool{
	"LomId":                       true,
	"LomIdView":                   true,
	"IsContentSelectedInDocument": true,
	"CurrentTime":                 true,
	"HighlightedTrackIndex":       true,
	"TimeSelection":               true,
	"ScrollerTimePreserver":       true,
	"NoteEditorFoldInZoom":        true,
	"NoteEditorFoldInScroll":      true,
	"NoteEditorFoldOutZoom":       true,
	"NoteEditorFoldOutScroll":     true,
	"NoteEditorFoldScaleZoom":     true,
	"NoteEditorFoldScaleScroll":   true,
	"TakeId":                      true,
	"OverwriteProtectionNumber":   true,
	"NextPointeeId":               true, // scalany osobno (remapIDs)
	// licznik ID nut w klipie - rosnie tez przy zmianach wprowadzanych przez
	// API Live'a; po merge'u ustawiany na max(NoteId)+1 (fixNoteIdGenerators)
	"NoteIdGenerator": true,
	// ustawienia paneli MIDI Tools (generatory/transformacje) - stan UI, Id
	// w PitchClasses przenumerowywane przy kazdym zapisie
	"NoteAlgorithms": true,
	// uzbrojenie przeskakuje za zaznaczona sciezka (base->ours: Bells true->false
	// po dodaniu nowej sciezki)
	"IsArmed": true,
	// licznik automatycznych kolorow - rosnie przy kazdym dodaniu sciezki
	"AutoColorPickerForPlayerAndGroupTracks": true,
	"AutoColorPickerForReturnAndMainTracks":  true,
	"ColorSequenceIndex":                     true,
	// przewiniecie i zoom Arrangement View
	"SequencerNavigator": true,
}

var noiseAttrs = map[string]map[string]bool{
	"FileRef":                {"Id": true},
	"NamedRemoteableKeyMidi": {"Id": true},
	"*": {
		"LomId":                      true,
		"SelectedToolPanel":          true,
		"SelectedTransformationName": true,
		"SelectedGeneratorName":      true,
	},
}

func isNoiseElem(n *Node) bool { return noiseElems[n.Name] }

func isNoiseAttr(elem, attr string) bool {
	return noiseAttrs["*"][attr] || noiseAttrs[elem][attr]
}

// --- Atomowe bloby -------------------------------------------------------------
//
// Stan wtyczek (VST2/VST3/AU) to nieprzezroczysty blob - porownywany w calosci,
// nigdy scalany wewnatrz.
var atomicElems = map[string]bool{
	"PluginDesc":      true,
	"ProcessorState":  true,
	"ControllerState": true,
	"Buffer":          true,
}

// --- Hash bez szumu --------------------------------------------------------------

type hasher struct{ memo map[*Node][20]byte }

func newHasher() *hasher { return &hasher{memo: map[*Node][20]byte{}} }

func (h *hasher) sum(n *Node) [20]byte {
	if s, ok := h.memo[n]; ok {
		return s
	}
	var b strings.Builder
	b.WriteString(n.Name)
	for _, a := range n.Attrs {
		if !isNoiseAttr(n.Name, a.Name) {
			b.WriteString("\x00" + a.Name + "=" + a.Value)
		}
	}
	b.WriteString("\x01" + n.Text)
	for _, c := range n.Children {
		if isNoiseElem(c) {
			continue
		}
		s := h.sum(c)
		b.Write(s[:])
	}
	s := sha1.Sum([]byte(b.String()))
	h.memo[n] = s
	return s
}

func (h *hasher) eq(a, b *Node) bool { return h.sum(a) == h.sum(b) }

// --- Jednostki konfliktu ---------------------------------------------------------
//
// Konflikt = obie strony zmienily cos w tej samej jednostce. Jednostka to
// najblizszy przodek bedacy: klipem, urzadzeniem, obwiednia automatyki, scena,
// lokatorem albo sciezka (dla zmian wlasnych sciezki: mikser, nazwa, kolor...).
// Poza sciezkami jednostka jest bezposrednie dziecko LiveSet (tempo/Transport,
// kolejnosc sciezek itd).

var trackElems = map[string]bool{
	"MidiTrack": true, "AudioTrack": true, "ReturnTrack": true, "GroupTrack": true,
	"MainTrack": true, "MasterTrack": true, "PreHearTrack": true,
}

func unitKind(n, parent *Node, depth int) string {
	switch {
	case trackElems[n.Name]:
		return "track"
	case n.Name == "MidiClip" || n.Name == "AudioClip":
		return "clip"
	case parent != nil && parent.Name == "Devices":
		return "device"
	case n.Name == "AutomationEnvelope":
		return "automation"
	case n.Name == "Scene":
		return "scene"
	case n.Name == "Locator":
		return "locator"
	case depth == 2: // Ableton > LiveSet > X
		return "global"
	}
	return ""
}

// --- Etykiety dla ludzi --------------------------------------------------------------

func trackName(n *Node) string {
	if v := n.Value("Name", "UserName"); v != "" {
		return v
	}
	return n.Value("Name", "EffectiveName")
}

func deviceName(n *Node) string {
	if v := n.Value("UserName"); v != "" {
		return v
	}
	if pd := n.Child("PluginDesc"); pd != nil && len(pd.Children) > 0 {
		info := pd.Children[0]
		for _, f := range []string{"Name", "PlugName"} {
			if v := info.Value(f); v != "" {
				return v
			}
		}
	}
	return ""
}

func label(n, parent *Node, key string) string {
	switch {
	case trackElems[n.Name]:
		return fmt.Sprintf("%s %q", n.Name, trackName(n))
	case n.Name == "ClipSlot" && parent != nil && parent.Name == "ClipSlotList":
		id, _ := n.Attr("Id")
		return "slot " + id
	case n.Name == "MidiClip" || n.Name == "AudioClip":
		s := n.Name
		if nm := n.Value("Name"); nm != "" {
			s += fmt.Sprintf(" %q", nm)
		}
		if t, ok := n.Attr("Time"); ok && t != "0" {
			s += " @" + t
		}
		return s
	case n.Name == "KeyTrack":
		return "key " + n.Value("MidiKey")
	case n.Name == "MidiNoteEvent":
		id, _ := n.Attr("NoteId")
		t, _ := n.Attr("Time")
		return "note #" + id + " t=" + t
	case parent != nil && parent.Name == "Devices":
		if nm := deviceName(n); nm != "" {
			return fmt.Sprintf("%s %q", n.Name, nm)
		}
		return n.Name
	case n.Name == "Scene":
		id, _ := n.Attr("Id")
		if nm := n.Value("Name"); nm != "" {
			return fmt.Sprintf("scene %s %q", id, nm)
		}
		return "scene " + id
	}
	return strings.TrimSuffix(key, "[0]")
}

// isLandmark: etykiety, ktore zostaja w skroconej sciezce (reszta to
// kontenery typu DeviceChain/MainSequencer, sklejane w jeden segment).
func isLandmark(n, parent *Node) bool {
	return trackElems[n.Name] || n.Name == "MidiClip" || n.Name == "AudioClip" ||
		n.Name == "KeyTrack" || n.Name == "MidiNoteEvent" || n.Name == "Scene" ||
		(n.Name == "ClipSlot" && parent != nil && parent.Name == "ClipSlotList") ||
		(parent != nil && parent.Name == "Devices")
}

// TrackNames: nazwy sciezek w kolejnosci jak song.tracks w API Live'a
// (sciezki powrotne i Main sa poza ta lista).
func TrackNames(d *Doc) []string {
	var out []string
	for _, t := range tracks(d) {
		if t.Name == "ReturnTrack" {
			continue
		}
		out = append(out, trackName(t))
	}
	return out
}

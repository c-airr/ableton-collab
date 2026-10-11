package als

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Sample, z ktorego korzysta projekt (tylko SampleRef - FileRef-y w presetach
// i OriginalFileRef to historia pochodzenia presetu, czesto ze sciezkami
// z komputerow autorow Abletona, i nie sa potrzebne do odtworzenia).
type Sample struct {
	Path     string // sciezka bezwzgledna zapisana przez Live'a
	Relative string // RelativePath
	Pack     string // LivePackName, gdy sample pochodzi z paczki Live'a
	Where    string // in-project, pack, external
}

// Samples zwraca unikalne sample projektu i ich klasyfikacje wzgledem
// folderu projektu (katalog, w ktorym lezy .als).
//
// Klasyfikacja nie uzywa RelativePathType: jego wartosci nie sa nigdzie
// udokumentowane (w testdata: 5 = paczka Live'a, 7 = wbudowane, 0 = brak/abs).
func Samples(d *Doc, projectDir string) []Sample {
	proj := normPath(projectDir)
	seen := map[string]bool{}
	var out []Sample
	d.Root.Walk(func(n *Node) {
		if n.Name != "SampleRef" {
			return
		}
		fr := n.Child("FileRef")
		if fr == nil {
			return
		}
		s := Sample{Path: fr.Value("Path"), Relative: fr.Value("RelativePath"), Pack: fr.Value("LivePackName")}
		if s.Path == "" && s.Relative == "" {
			return
		}
		key := s.Path + "|" + s.Relative
		if seen[key] {
			return
		}
		seen[key] = true
		switch {
		case proj != "" && strings.HasPrefix(normPath(s.Path)+"/", proj+"/"):
			s.Where = "in-project"
		case s.Pack != "":
			s.Where = "pack"
		default:
			s.Where = "external"
		}
		out = append(out, s)
	})
	return out
}

func normPath(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.ToSlash(filepath.Clean(p))
	return strings.ToLower(strings.TrimSuffix(p, "/"))
}

// Plugins zwraca nazwy wtyczek (VST2/VST3/AU) uzytych w projekcie.
func Plugins(d *Doc) []string {
	set := map[string]bool{}
	d.Root.Walk(func(n *Node) {
		var name string
		switch n.Name {
		case "Vst3PluginInfo", "AuPluginInfo":
			name = n.Value("Name")
		case "VstPluginInfo":
			name = n.Value("PlugName")
		default:
			return
		}
		if name != "" {
			set[strings.TrimSuffix(n.Name, "PluginInfo")+": "+name] = true
		}
	})
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// AssetWarnings: to, co nie dotrze do wspolpracownika razem z folderem projektu.
func AssetWarnings(d *Doc, projectDir string) []Warning {
	var out []Warning
	packs := map[string]int{}
	for _, s := range Samples(d, projectDir) {
		switch s.Where {
		case "external":
			out = append(out, Warning{"external-sample", fmt.Sprintf(
				"sample outside the session folder: %s - use File > Collect All and Save", s.Path)})
		case "pack":
			packs[s.Pack]++
		}
	}
	for p, n := range packs {
		if p == "Core Library" {
			continue // Core Library ma kazdy Live (zawartosc zalezy od edycji)
		}
		out = append(out, Warning{"pack-sample", fmt.Sprintf(
			"%d samples from pack %q - everyone needs it installed", n, p)})
	}
	if ps := Plugins(d); len(ps) > 0 {
		out = append(out, Warning{"plugins", "plugins in the set (everyone needs them): " + strings.Join(ps, ", ")})
	}
	return out
}

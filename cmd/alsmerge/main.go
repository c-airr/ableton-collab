// alsmerge scala trzy wersje projektu Ableton Live: base ours theirs -o merged.als
// Kod wyjscia: 0 - scalono, 1 - nierozwiazany konflikt (plik wynikowy nie
// powstaje), 2 - blad.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/c-airr/ableton-collab/als"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(s string) error { *m = append(*m, s); return nil }

func main() {
	out := flag.String("o", "", "plik wynikowy .als")
	interactive := flag.Bool("i", false, "pytaj o kazdy konflikt (ours/theirs/both)")
	var rs multi
	flag.Var(&rs, "r", "rozstrzygniecie konfliktu: ID=ours|theirs|both (mozna wiele razy)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "uzycie: alsmerge base.als ours.als theirs.als -o merged.als [-i] [-r ID=ours]")
		flag.PrintDefaults()
	}
	// pozwalamy na flagi po argumentach pozycyjnych
	var pos []string
	args := os.Args[1:]
	for len(args) > 0 {
		flag.CommandLine.Parse(args)
		if flag.NArg() == 0 {
			break
		}
		pos = append(pos, flag.Arg(0))
		args = flag.Args()[1:]
	}
	if len(pos) != 3 || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	var docs [3]*als.Doc
	for i, p := range pos {
		d, err := als.ReadFile(p)
		if err != nil {
			fail(err)
		}
		docs[i] = d
	}
	res := map[string]als.Resolution{}
	for _, r := range rs {
		id, choice, ok := strings.Cut(r, "=")
		if !ok {
			fail(fmt.Errorf("-r %q: oczekiwano ID=wybor", r))
		}
		c, err := als.ParseResolution(choice)
		if err != nil {
			fail(err)
		}
		res[id] = c
	}

	r, err := als.MergeWith(docs[0], docs[1], docs[2], res)
	if err != nil {
		fail(err)
	}
	if len(r.Conflicts) > 0 && *interactive {
		in := bufio.NewScanner(os.Stdin)
		for _, c := range r.Conflicts {
			fmt.Fprint(os.Stderr, c)
			res[c.ID] = ask(in, c)
		}
		if r, err = als.MergeWith(docs[0], docs[1], docs[2], res); err != nil {
			fail(err)
		}
	}
	if len(r.Conflicts) > 0 {
		fmt.Fprintf(os.Stderr, "%d konflikt(y) - plik %s NIE zostal zapisany\n\n", len(r.Conflicts), *out)
		for _, c := range r.Conflicts {
			fmt.Fprint(os.Stderr, c)
			opts := "ours|theirs"
			if c.CanBoth {
				opts += "|both"
			}
			fmt.Fprintf(os.Stderr, "  rozstrzygniecie: -r '%s=%s'\n\n", c.ID, opts)
		}
		os.Exit(1)
	}
	if errs := als.Validate(r.Doc); len(errs) > 0 {
		fmt.Fprintln(os.Stderr, "blad: wynik niespojny, nie zapisuje:")
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, "  "+e)
		}
		os.Exit(2)
	}
	warnings := append(r.Warnings, als.AssetWarnings(r.Doc, filepath.Dir(pos[1]))...)
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, w)
	}
	if err := r.Doc.WriteFile(*out); err != nil {
		fail(err)
	}
	fmt.Println("scalono ->", *out)
}

func ask(in *bufio.Scanner, c als.Conflict) als.Resolution {
	prompt := "  [o] zachowaj moje  [t] zachowaj ich"
	if c.CanBoth {
		prompt += "  [b] zachowaj oba (klip ich do wolnego slotu)"
	}
	for {
		fmt.Fprint(os.Stderr, prompt+": ")
		if !in.Scan() {
			fail(fmt.Errorf("przerwano"))
		}
		r, err := als.ParseResolution(in.Text())
		if err == nil && (r != als.Both || c.CanBoth) {
			return r
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "blad:", err)
	os.Exit(2)
}

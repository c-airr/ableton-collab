// alsdiff pokazuje strukturalne roznice miedzy dwoma plikami .als.
// Kod wyjscia: 0 - brak roznic, 1 - sa roznice, 2 - blad.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/c-airr/ableton-collab/als"
)

func main() {
	check := flag.Bool("check", false, "also list samples/plugins collaborators won't have (for b.als)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: alsdiff [-check] a.als b.als")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 2 {
		flag.Usage()
		os.Exit(2)
	}
	a, err := als.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	b, err := als.ReadFile(flag.Arg(1))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	// ta sama nuta z innym NoteId (dryf po API Live'a) to nie zmiana
	als.NormalizeNoteIDs(a, b)
	ch := als.Diff(a, b)
	for _, c := range ch {
		fmt.Println(c)
	}
	if *check {
		for _, w := range als.AssetWarnings(b, filepath.Dir(flag.Arg(1))) {
			fmt.Println(w)
		}
	}
	if len(ch) > 0 {
		os.Exit(1)
	}
}

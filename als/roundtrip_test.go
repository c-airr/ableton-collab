package als

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func rawXML(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Parse -> zapis musi dac identyczne bajty, inaczej merge wprowadzalby wlasny szum.
func TestRoundtripRealFiles(t *testing.T) {
	files, _ := filepath.Glob("../testdata/*.als")
	if len(files) == 0 {
		t.Skip("brak plikow w testdata")
	}
	for _, p := range files {
		want := rawXML(t, p)
		d, err := ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		got := d.XMLBytes()
		if !bytes.Equal(got, want) {
			i := 0
			for i < len(got) && i < len(want) && got[i] == want[i] {
				i++
			}
			lo := max(0, i-80)
			t.Errorf("%s: roznica na bajcie %d\nwant: %q\ngot:  %q", p, i, want[lo:min(len(want), i+80)], got[lo:min(len(got), i+80)])
		}
	}
}

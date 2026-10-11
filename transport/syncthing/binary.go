package syncthing

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Version: przypieta wersja Syncthinga. REST API "moze sie zmieniac" miedzy
// wersjami, wiec nie bierzemy "najnowszej" - zmiana wersji = zmiana tutaj
// razem z sumami z sha256sum.txt.asc wydania.
const Version = "v2.1.6"

// sums: sha256 archiwow z https://github.com/syncthing/syncthing/releases/tag/v2.1.6
var sums = map[string]string{
	"windows-amd64":   "589098269cabb9a5a1703a934305b4153609d7623e978e1c94f0f49d77ae5e9f",
	"windows-arm64":   "ddb25a3cf556157e329263bd3331d889ea7bb952b85a5f530320175105bab072",
	"linux-amd64":     "524ef4e1df1850b719e2378c8369956f4e5f2ab9a46fdf7f45f2b925012147aa",
	"linux-arm64":     "f054de02462d5d58bd126908f2f92b63aba74f162a5be85e50e96d656e7db47d",
	"macos-universal": "83d2898de5607d12c25d66856b1e3aa40326814e8ded6af98ab8ba68c2a545bc",
}

// platform: nazwa platformy w nazwach archiwow Syncthinga.
func platform() (string, error) {
	p := runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "darwin" {
		p = "macos-universal"
	}
	if _, ok := sums[p]; !ok {
		return "", fmt.Errorf("brak Syncthinga dla platformy %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return p, nil
}

func archiveName(plat string) string {
	ext := ".tar.gz"
	if plat != "linux-amd64" && plat != "linux-arm64" {
		ext = ".zip"
	}
	return "syncthing-" + plat + "-" + Version + ext
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "syncthing.exe"
	}
	return "syncthing"
}

// Ensure zwraca sciezke do binarki Syncthinga w dir, pobierajac ja przy
// pierwszym uruchomieniu (archiwum sprawdzane sha256 przed rozpakowaniem).
// ABCOLLAB_SYNCTHING wskazuje gotowa binarke (testy, wlasna instalacja).
func Ensure(dir string, log func(string)) (string, error) {
	if p := os.Getenv("ABCOLLAB_SYNCTHING"); p != "" {
		return p, nil
	}
	plat, err := platform()
	if err != nil {
		return "", err
	}
	exe := filepath.Join(dir, "syncthing-"+Version, exeName())
	if _, err := os.Stat(exe); err == nil {
		return exe, nil
	}
	name := archiveName(plat)
	url := "https://github.com/syncthing/syncthing/releases/download/" + Version + "/" + name
	if log != nil {
		log("pobieranie Syncthing " + Version + " (jednorazowo, ok. 12 MB)...")
	}
	data, err := download(url)
	if err != nil {
		return "", fmt.Errorf("pobieranie Syncthinga: %w", err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != sums[plat] {
		return "", fmt.Errorf("pobrany %s ma zla sume sha256 (%s) - nie uruchamiam", name, got)
	}
	bin, err := extract(name, exeName(), data)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		return "", err
	}
	tmp := exe + ".tmp"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return exe, nil
}

func download(url string) ([]byte, error) {
	c := &http.Client{Timeout: 5 * time.Minute}
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

// topLevel: plik lezy bezposrednio w glownym katalogu archiwum. W archiwum
// sa tez inne pliki o nazwie "syncthing" (np. etc/*/syncthing - skrypty uslug).
func topLevel(p, want string) bool {
	p = strings.TrimPrefix(p, "./")
	return path.Base(p) == want && strings.Count(strings.Trim(p, "/"), "/") == 1
}

// extract wyciaga sama binarke z archiwum wydania (<katalog>/syncthing[.exe]).
func extract(name, want string, data []byte) ([]byte, error) {
	if path.Ext(name) == ".zip" {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if topLevel(f.Name, want) && !f.FileInfo().IsDir() {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
		}
	} else {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if topLevel(h.Name, want) && h.Typeflag == tar.TypeReg {
				return io.ReadAll(tr)
			}
		}
	}
	return nil, fmt.Errorf("%s: brak %s w archiwum", name, want)
}

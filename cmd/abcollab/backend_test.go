package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDocumentsDirXDG(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG user dirs are Linux-only")
	}
	home := t.TempDir()
	conf := filepath.Join(home, ".config")
	os.MkdirAll(conf, 0o755)
	t.Setenv("XDG_CONFIG_HOME", conf)
	if got := documentsDir(home); got != filepath.Join(home, "Documents") {
		t.Fatalf("no user-dirs: %s", got)
	}
	os.WriteFile(filepath.Join(conf, "user-dirs.dirs"), []byte("# x\nXDG_DESKTOP_DIR=\"$HOME/Pulpit\"\nXDG_DOCUMENTS_DIR=\"$HOME/Dokumenty\"\n"), 0o644)
	if got := documentsDir(home); got != filepath.Join(home, "Dokumenty") {
		t.Fatalf("Polish desktop: %s", got)
	}
}

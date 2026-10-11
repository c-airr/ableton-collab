package live

import (
	"testing"
	"unsafe"
)

// SendInput wymaga dokladnie rozmiaru INPUT z Windows (40 bajtow na 64 bitach).
func TestKeyInputSize(t *testing.T) {
	if s := unsafe.Sizeof(keyInput{}); s != 40 {
		t.Fatalf("INPUT size %d", s)
	}
}

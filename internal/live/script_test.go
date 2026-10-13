package live

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ScriptVersion w Go = VERSION w skrypcie.
func TestScriptVersion(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("script", "AbletonCollab", "__init__.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), fmt.Sprintf("\nVERSION = %d\n", ScriptVersion)) {
		t.Fatalf("script VERSION differs from ScriptVersion %d", ScriptVersion)
	}
}

// Skrypt AbletonCollab na atrapie API Live'a (script/test_script.py).
func TestScriptWithFakeLive(t *testing.T) {
	py := ""
	for _, name := range []string{"python3", "python"} {
		// na Windows "python3" bywa atrapa ze Sklepu - sprawdzamy, czy dziala
		if p, err := exec.LookPath(name); err == nil && exec.Command(p, "-c", "pass").Run() == nil {
			py = p
			break
		}
	}
	if py == "" {
		t.Skip("no python")
	}
	out, err := exec.Command(py, filepath.Join("script", "test_script.py")).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

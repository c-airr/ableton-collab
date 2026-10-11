package live

import (
	"os/exec"
	"path/filepath"
	"testing"
)

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

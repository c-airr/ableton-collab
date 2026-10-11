//go:build !windows

package syncthing

import (
	"os/exec"
	"syscall"
)

// detach: wlasna grupa procesow, zeby Ctrl+C w terminalu nie zabijal Syncthinga
// w polowie zapisu - zamykamy go sami przez REST.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

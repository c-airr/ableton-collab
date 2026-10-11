package syncthing

import (
	"os/exec"
	"syscall"
)

// detach: wlasna grupa procesow, zeby Ctrl+C w konsoli nie zabijal Syncthinga
// w polowie zapisu - zamykamy go sami przez REST.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

//go:build windows

package launcher

import (
	"os/exec"
	"syscall"
)

func hideDockerCommand(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }

//go:build !windows

package launcher

import "os/exec"

func hideDockerCommand(cmd *exec.Cmd) {}

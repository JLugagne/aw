//go:build !unix

package cli

import (
	"os"
	"os/exec"
)

func execReplace(path string, argv []string, dir string, env []string) error {
	cmd := exec.Command(path, argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

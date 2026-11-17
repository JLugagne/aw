//go:build unix

package cli

import (
	"os"
	"syscall"
)

func execReplace(path string, argv []string, dir string, env []string) error {
	if err := os.Chdir(dir); err != nil {
		return err
	}
	return syscall.Exec(path, argv, env)
}

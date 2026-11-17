// Command aw manages multi-repository coding-agent sessions.
package main

import (
	"os"
	"runtime/debug"
	"strings"

	"github.com/JLugagne/aw/internal/cli"
)

var version = "dev"

func main() {
	os.Exit(cli.Execute(resolveVersion()))
}

func resolveVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return strings.TrimPrefix(info.Main.Version, "v")
	}
	return version
}

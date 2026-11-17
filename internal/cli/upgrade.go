package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/JLugagne/aw/internal/upgrade"
)

func (a *App) upgradeCmd() *cobra.Command {
	var check, force, source bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade aw to the latest release",
		Long: `Download the latest GitHub release of aw for this platform, verify its
checksum and replace the running binary. With --source, or when no release
is published, fall back to "go install ` + upgrade.ModulePath + `".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if source {
				return a.goInstall()
			}
			u := upgrade.Updater{}
			rel, err := u.Latest(cmd.Context())
			if errors.Is(err, upgrade.ErrNoRelease) {
				fmt.Fprintln(a.Err, "no release published yet, building from source")
				return a.goInstall()
			}
			if err != nil {
				return err
			}
			if rel.Version() == a.Version && !force {
				fmt.Fprintf(a.Out, "aw %s is up to date\n", a.Version)
				return nil
			}
			if check {
				fmt.Fprintf(a.Out, "update available: %s -> %s\n", a.Version, rel.Version())
				return nil
			}
			exe, err := upgrade.Executable()
			if err != nil {
				return err
			}
			if err := u.Install(cmd.Context(), rel, exe); err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "aw upgraded %s -> %s (%s)\n", a.Version, rel.Version(), exe)
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report whether an update is available")
	cmd.Flags().BoolVar(&force, "force", false, "reinstall even if already on the latest version")
	cmd.Flags().BoolVar(&source, "source", false, "build the latest version with go install instead of downloading a release")
	return cmd
}

func (a *App) goInstall() error {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return errors.Join(errors.New("go toolchain not found; install aw from a release with install.sh"), err)
	}
	cmd := exec.Command(goBin, "install", upgrade.ModulePath)
	cmd.Stdout = a.Out
	cmd.Stderr = a.Err
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		return err
	}
	fmt.Fprintln(a.Out, "installed with go install (into $GOBIN or $GOPATH/bin)")
	return nil
}

package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/example-user/commander/internal/binstage"
)

const installUsage = `usage: cmdr install [--yes] [--restart-cmdrd] [--restart-only] [--rollback] [--from <staged dir>]

Copies the binaries staged by "just build" (bin/staged) into place as real files
under $CMDR_INSTALL_DIR (default $COMMANDER_HOME/bin) and points the
~/.local/bin/{cmdr,cmdrd,commander} links there. The previous binaries are kept
as .prev; --rollback swaps them back. cmdrd is restarted only with --restart-cmdrd,
which after installing the already-installed revision copies nothing and keeps .prev.
--restart-only just restarts cmdrd (no copy, no .prev change).
Both restarts also SIGTERM any other cmdrd daemon of this user (e.g. one the app
started; chat hosts and bridges are left alone), SIGKILL after 15s with a message.
Refused from an agent (COMMANDER_ROLE set); asks to confirm unless --yes.`

// restartCmdrdFn restarts the cmdrd service; replaced in tests.
var restartCmdrdFn = restartCmdrd

func runInstall(args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	fs := flag.NewFlagSet("cmdr install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	restart := fs.Bool("restart-cmdrd", false, "restart cmdrd after installing")
	restartOnly := fs.Bool("restart-only", false, "only restart cmdrd; install nothing")
	rollback := fs.Bool("rollback", false, "restore the previous installed binaries")
	from := fs.String("from", "", "staged directory (default <repo>/bin/staged)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "cmdr install: unknown argument: %s\n%s\n", fs.Arg(0), installUsage)
		return 2
	}
	if role := os.Getenv("COMMANDER_ROLE"); role != "" {
		fmt.Fprintf(stderr, "cmdr install: refused for agent role %s; installing needs the user's OK\n", role)
		return 1
	}
	if *restartOnly {
		if *rollback || *restart || *from != "" {
			fmt.Fprintln(stderr, "cmdr install: --restart-only takes no other flags except --yes")
			return 2
		}
		if !*yes && !confirm(stdin, stdout, stderr, "restart cmdrd? [y/N] ") {
			return 1
		}
		if err := restartAndStopStrays(stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "cmdr install: restarting cmdrd failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "restarted cmdrd")
		return 0
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "cmdr install: %v\n", err)
		return 1
	}
	opts := binstage.Options{
		InstallDir: binstage.InstallDir(os.Getenv, home),
		LocalBin:   filepath.Join(home, ".local", "bin"),
		Out:        stdout,
	}
	verb := "install"
	if *rollback {
		verb = "roll back"
	} else {
		repo := resolveCommanderRepo()
		opts.Repo = repo
		opts.StageDir = *from
		if opts.StageDir == "" {
			if repo == "" {
				fmt.Fprintln(stderr, "cmdr install: could not resolve the commander repo; set COMMANDER_REPO or pass --from")
				return 1
			}
			opts.StageDir = filepath.Join(repo, "bin", "staged")
		}
	}
	if !*yes && !confirm(stdin, stdout, stderr, fmt.Sprintf("%s into %s and relink %s? [y/N] ", verb, opts.InstallDir, opts.LocalBin)) {
		return 1
	}
	var runErr error
	if *rollback {
		runErr = binstage.Rollback(opts)
	} else {
		runErr = binstage.Install(opts)
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "cmdr install: %v\n", runErr)
		return 1
	}
	if *restart {
		if err := restartAndStopStrays(stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "cmdr install: installed, but restarting cmdrd failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "restarted cmdrd")
	} else {
		fmt.Fprintln(stdout, "cmdrd left running on the old binary; restart it with `cmdr install --restart-only`")
	}
	return 0
}

func confirm(stdin io.Reader, stdout, stderr io.Writer, prompt string) bool {
	fmt.Fprint(stdout, prompt)
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
		fmt.Fprintln(stderr, "cmdr install: not confirmed, nothing changed")
		return false
	}
	return true
}

func restartCmdrd() error {
	if runtime.GOOS == "darwin" {
		return exec.Command("launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/org.commander", os.Getuid())).Run()
	}
	return exec.Command("systemctl", "--user", "restart", "commander.service").Run()
}

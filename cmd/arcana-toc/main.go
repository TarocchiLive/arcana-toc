// Command arcana-toc is the entry point of the Arcana ToC jukebox service:
// config → storage → control HTTP, with graceful shutdown on SIGINT/SIGTERM.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/LingLambda/arcana-toc/internal/config"
	"github.com/LingLambda/arcana-toc/internal/tui"
	"github.com/LingLambda/arcana-toc/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "arcana-toc:", err)
		os.Exit(1)
	}
}

func run() error { return runArgs(os.Args[1:]) }

func runArgs(args []string) error {
	flags := flag.NewFlagSet("arcana-toc", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "", "path to YAML config file (empty: ~/.arcana/toc/config.yaml)")
	showVersion := flags.Bool("version", false, "print build information and exit")
	showTUI := flags.Bool("tui", false, "open the terminal interface for the running service")
	importCookies := flags.String("import-netease-cookies", "", "import a local Netscape session file and exit")
	login := flags.Bool("login-netease", false, "show QR in terminal and complete login through the running service")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if *showVersion {
		fmt.Println("arcana-toc", version.String())
		return nil
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if (*showTUI && (*login || *importCookies != "")) || (*login && *importCookies != "") {
		return errors.New("--tui, --login-netease and --import-netease-cookies are mutually exclusive")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *showTUI {
		return tui.Run(cfg)
	}
	if *login {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		return loginTerminal(ctx, cfg, os.Stdout)
	}
	return runService(cfg, *importCookies)
}

// Command leasorixd is the Leasorix DHCP server daemon.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/meister119/leasorix/internal/app"
	"github.com/meister119/leasorix/internal/buildinfo"
	"github.com/meister119/leasorix/internal/config"
	"github.com/meister119/leasorix/internal/logging"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("leasorixd", flag.ContinueOnError)
	configPath := fs.String("config", os.Getenv("LEASORIX_CONFIG"), "path to the YAML configuration file (env LEASORIX_CONFIG)")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Println(buildinfo.String())
		return 0
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leasorixd: %v\n", err)
		return 1
	}
	var out io.Writer = os.Stderr
	if cfg.Log.File != "" {
		rf, err := logging.OpenRotating(cfg.Log.File, cfg.Log.MaxSizeMB, cfg.Log.MaxBackups)
		if err != nil {
			fmt.Fprintf(os.Stderr, "leasorixd: open log file: %v\n", err)
			return 1
		}
		defer rf.Close()
		out = rf
	}
	log, err := logging.New(out, cfg.Log.Level, cfg.Log.Format)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leasorixd: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg, log)
	if err != nil {
		log.Error("startup failed", "err", err)
		return 1
	}
	if err := a.Run(ctx); err != nil {
		log.Error("terminated with error", "err", err)
		return 1
	}
	return 0
}

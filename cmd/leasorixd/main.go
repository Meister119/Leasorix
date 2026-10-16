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
	"github.com/meister119/leasorix/internal/ha"
	"github.com/meister119/leasorix/internal/logging"
	"github.com/meister119/leasorix/web"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

const usage = `Usage:
  leasorixd [--config FILE]                      run the DHCP server
  leasorixd --version                            print the version
  leasorixd ha certs --out DIR NODE_ID NODE_ID   generate a CA and node certificates for HA (mtls)
  leasorixd leases export [--config FILE] [--out FILE]
                                                 export the lease table as JSON (also while running)
  leasorixd leases import [--config FILE] FILE   merge a lease export (server must be stopped)
`

func run(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "ha":
			return runHA(args[1:])
		case "leases":
			return runLeases(args[1:])
		case "help", "-h", "--help":
			fmt.Print(usage)
			return 0
		}
	}
	return serve(args)
}

func serve(args []string) int {
	fs := flag.NewFlagSet("leasorixd", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), usage); fs.PrintDefaults() }
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

	a, err := app.New(ctx, cfg, log, app.WithUI(web.Handler()))
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

func runHA(args []string) int {
	if len(args) == 0 || args[0] != "certs" {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet("leasorixd ha certs", flag.ContinueOnError)
	out := fs.String("out", "", "output directory (must not contain the files yet)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *out == "" || fs.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "leasorixd ha certs: --out and at least two node ids are required")
		return 2
	}
	b, err := ha.GenerateCerts(fs.Args())
	if err != nil {
		fmt.Fprintf(os.Stderr, "leasorixd ha certs: %v\n", err)
		return 1
	}
	files, err := b.WriteFiles(*out)
	for _, f := range files {
		fmt.Println("wrote", f)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "leasorixd ha certs: %v\n", err)
		return 1
	}
	fmt.Printf("certificates valid until %s; copy ca.pem, <node>.pem and <node>-key.pem to each node\n", b.NotAfter.Format("2006-01-02"))
	return 0
}

func runLeases(args []string) int {
	if len(args) == 0 || (args[0] != "export" && args[0] != "import") {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	cmd := args[0]
	fs := flag.NewFlagSet("leasorixd leases "+cmd, flag.ContinueOnError)
	configPath := fs.String("config", os.Getenv("LEASORIX_CONFIG"), "path to the YAML configuration file (env LEASORIX_CONFIG)")
	outPath := fs.String("out", "", "export: output file (default stdout)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leasorixd: %v\n", err)
		return 1
	}
	ctx := context.Background()
	if cmd == "export" {
		var w io.Writer = os.Stdout
		if *outPath != "" {
			f, err := os.OpenFile(*outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // operator-supplied path
			if err != nil {
				fmt.Fprintf(os.Stderr, "leasorixd: %v\n", err)
				return 1
			}
			defer f.Close()
			w = f
		}
		n, err := app.ExportLeases(ctx, cfg, w)
		if err != nil {
			fmt.Fprintf(os.Stderr, "leasorixd leases export: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "exported %d lease records\n", n)
		return 0
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "leasorixd leases import: exactly one file is required")
		return 2
	}
	f, err := os.Open(fs.Arg(0)) //nolint:gosec // operator-supplied path
	if err != nil {
		fmt.Fprintf(os.Stderr, "leasorixd: %v\n", err)
		return 1
	}
	defer f.Close()
	log, _ := logging.New(os.Stderr, "warn", "text")
	recs, changed, conflicts, err := app.ImportLeases(ctx, cfg, log, f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leasorixd leases import: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "imported %d records: %d changed, %d conflicts resolved\n", recs, changed, conflicts)
	return 0
}

// Command leasorixctl manages a Leasorix server through its REST API.
//
//	leasorixctl --url https://dhcp01:8067 --token lxt_... scopes list
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/meister119/leasorix/internal/api/gen"
	"github.com/meister119/leasorix/internal/buildinfo"
)

const usage = `Usage: leasorixctl [flags] <command> [arguments]

Commands:
  status                                server, HA and scope utilisation overview
  scopes list                           list scopes
  scopes show <id|name>                 one scope with its statistics
  scopes export <id|name> [-f FILE]     scope bundle (JSON)
  scopes import <FILE>                  create a scope from a bundle
  leases list [--scope S] [--state S] [-q TEXT] [--limit N]
  leases show <ip>
  leases delete <ip>
  leases export [-f FILE]               emergency export of the lease table
  leases import <FILE>                  merge a lease export (admin)
  ha status                             high availability status
  ha partner-down --yes                 confirm that the partner is down (admin)
  config export [--format yaml|json] [-f FILE]
  config import <FILE> [--mode merge|replace] [--dry-run] [--netsh]
  backup <FILE>                         download a complete backup (admin)
  restore <FILE> --yes                  restore a backup (admin; ends all sessions)
  version

Flags (also as environment variables):
`

type ctl struct {
	out, errOut io.Writer
	c           *gen.ClientWithResponses
	json        bool
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("leasorixctl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	def := func(env, fallback string) string {
		if v := getenv(env); v != "" {
			return v
		}
		return fallback
	}
	url := fs.String("url", def("LEASORIX_URL", "https://127.0.0.1:8067"), "server URL (LEASORIX_URL)")
	token := fs.String("token", getenv("LEASORIX_TOKEN"), "API token lxt_... (LEASORIX_TOKEN)")
	insecure := fs.Bool("insecure", getenv("LEASORIX_INSECURE") == "1", "do not verify the TLS certificate (LEASORIX_INSECURE=1)")
	caFile := fs.String("ca", getenv("LEASORIX_CA"), "PEM file with the server certificate or CA to trust (LEASORIX_CA)")
	output := fs.String("o", def("LEASORIX_OUTPUT", "table"), "output format: table or json (LEASORIX_OUTPUT)")
	timeout := fs.Duration("timeout", 60*time.Second, "request timeout")
	fs.Usage = func() {
		fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return 2
	}
	if rest[0] == "version" {
		fmt.Fprintln(stdout, "leasorixctl", buildinfo.String())
		return 0
	}
	if *output != "table" && *output != "json" {
		fmt.Fprintln(stderr, "leasorixctl: -o must be table or json")
		return 2
	}
	if *token == "" {
		fmt.Fprintln(stderr, "leasorixctl: an API token is required (--token or LEASORIX_TOKEN); create one in the web UI under Benutzer & Einstellungen")
		return 2
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: *insecure} //nolint:gosec // opt-in for self-signed certificates
	if *caFile != "" {
		pem, err := os.ReadFile(*caFile)
		if err != nil {
			fmt.Fprintf(stderr, "leasorixctl: %v\n", err)
			return 1
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			fmt.Fprintln(stderr, "leasorixctl: no certificate found in", *caFile)
			return 1
		}
		tlsCfg.RootCAs = pool
	}
	hc := &http.Client{Timeout: *timeout, Transport: &http.Transport{TLSClientConfig: tlsCfg, Proxy: http.ProxyFromEnvironment}}
	auth := func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+*token)
		return nil
	}
	client, err := gen.NewClientWithResponses(strings.TrimRight(*url, "/")+"/api/v1", gen.WithHTTPClient(hc), gen.WithRequestEditorFn(auth))
	if err != nil {
		fmt.Fprintf(stderr, "leasorixctl: %v\n", err)
		return 1
	}
	c := &ctl{out: stdout, errOut: stderr, c: client, json: *output == "json"}
	err = c.dispatch(ctx, rest)
	var ue usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ue):
		fmt.Fprintf(stderr, "leasorixctl: %v\n\n", err)
		fs.Usage()
		return 2
	default:
		fmt.Fprintf(stderr, "leasorixctl: %v\n", err)
		return 1
	}
}

type usageError string

func (u usageError) Error() string { return string(u) }

// apiError converts a non-2xx response into an error with the server's
// message.
func apiError(status int, body []byte) error {
	var e gen.Error
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		msg := e.Error
		if e.Fields != nil {
			for _, f := range *e.Fields {
				msg += fmt.Sprintf("\n  %s: %s", f.Field, f.Message)
			}
		}
		return fmt.Errorf("server answered %d: %s", status, msg)
	}
	return fmt.Errorf("server answered %d: %s", status, strings.TrimSpace(string(body)))
}

type response interface{ StatusCode() int }

// check returns an error for transport errors and non-2xx answers. r is a
// generated *XxxResponse (with a Body field).
func check(r response, err error) error {
	if err != nil {
		return err
	}
	if s := r.StatusCode(); s < 200 || s > 299 {
		var body []byte
		if v := reflect.ValueOf(r); v.Kind() == reflect.Pointer && !v.IsNil() {
			if f := v.Elem().FieldByName("Body"); f.IsValid() && f.Kind() == reflect.Slice {
				body = f.Bytes()
			}
		}
		return apiError(s, body)
	}
	return nil
}

func (c *ctl) printJSON(v any) error {
	enc := json.NewEncoder(c.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// writeOut writes data to the named file (or stdout for "" / "-").
func (c *ctl) writeOut(path string, data []byte) error {
	if path == "" || path == "-" {
		_, err := c.out.Write(data)
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(c.errOut, "written to %s (%d bytes)\n", path, len(data))
	return nil
}

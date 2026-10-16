package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/meister119/leasorix/internal/api/gen"
)

// parse parses flags that may appear before, between or after positional
// arguments and returns the positional ones.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, usageError(err.Error())
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func (c *ctl) dispatch(ctx context.Context, args []string) error {
	cmd, rest := args[0], args[1:]
	sub := ""
	if len(rest) > 0 {
		sub = rest[0]
	}
	switch cmd {
	case "status":
		return c.status(ctx)
	case "scopes":
		switch sub {
		case "list", "":
			return c.scopesList(ctx)
		case "show":
			return c.scopesShow(ctx, rest[1:])
		case "export":
			return c.scopesExport(ctx, rest[1:])
		case "import":
			return c.scopesImport(ctx, rest[1:])
		}
	case "leases":
		switch sub {
		case "list", "":
			return c.leasesList(ctx, rest[min(1, len(rest)):])
		case "show":
			return c.leasesShow(ctx, rest[1:])
		case "delete":
			return c.leasesDelete(ctx, rest[1:])
		case "export":
			return c.leasesExport(ctx, rest[1:])
		case "import":
			return c.leasesImport(ctx, rest[1:])
		}
	case "ha":
		switch sub {
		case "status", "":
			return c.haStatus(ctx)
		case "partner-down":
			return c.partnerDown(ctx, rest[1:])
		}
	case "dns":
		switch sub {
		case "zones":
			return c.dnsZones(ctx)
		case "registrations", "":
			return c.dnsRegistrations(ctx)
		}
	case "v6":
		return c.v6(ctx, rest)
	case "config":
		switch sub {
		case "export":
			return c.configExport(ctx, rest[1:])
		case "import":
			return c.configImport(ctx, rest[1:])
		}
	case "backup":
		return c.backup(ctx, rest)
	case "restore":
		return c.restore(ctx, rest)
	}
	return usageError(fmt.Sprintf("unknown command %q", strings.Join(args, " ")))
}

func (c *ctl) table(header string, rows [][]string) {
	tw := tabwriter.NewWriter(c.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, header)
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	_ = tw.Flush()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func duration(seconds int) string {
	if seconds == 0 {
		return "unlimited"
	}
	return (time.Duration(seconds) * time.Second).String()
}

// ---- status -------------------------------------------------------------------------

func (c *ctl) status(ctx context.Context) error {
	sys, err := c.c.GetSystemInfoWithResponse(ctx)
	if err := check(sys, err); err != nil {
		return err
	}
	ha, err := c.c.GetHAStatusWithResponse(ctx)
	if err := check(ha, err); err != nil {
		return err
	}
	st, err := c.c.GetStatsWithResponse(ctx)
	if err := check(st, err); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(map[string]any{"system": sys.JSON200, "ha": ha.JSON200, "stats": st.JSON200})
	}
	s, h, stats := sys.JSON200, ha.JSON200, st.JSON200
	fmt.Fprintf(c.out, "Version:        %s (%s)\n", s.Version, s.Commit)
	fmt.Fprintf(c.out, "Running since:  %s\n", s.StartedAt.Local().Format(time.RFC3339))
	fmt.Fprintf(c.out, "DHCP interfaces: %s\n", strings.Join(s.Dhcp4Interfaces, ", "))
	if len(s.Dhcp6Interfaces) > 0 {
		fmt.Fprintf(c.out, "DHCPv6:         %s (DUID %s)\n", strings.Join(s.Dhcp6Interfaces, ", "), deref(s.Dhcp6ServerDuid))
	}
	if h.Enabled {
		peer := "not connected"
		if h.Connected != nil && *h.Connected {
			peer = "connected"
		}
		fmt.Fprintf(c.out, "HA:             %s, partner %s %s\n", deref(h.State), deref(h.PeerId), peer)
	} else {
		fmt.Fprintln(c.out, "HA:             not configured")
	}
	fmt.Fprintf(c.out, "Active leases:  %d\n\n", stats.ActiveLeases)
	rows := make([][]string, 0, len(stats.Scopes))
	for _, sc := range stats.Scopes {
		rows = append(rows, []string{sc.Name, sc.Subnet, strconv.FormatInt(sc.Usable-sc.Available, 10) + "/" + strconv.FormatInt(sc.Usable, 10),
			fmt.Sprintf("%.1f %%", sc.UtilizationPercent)})
	}
	c.table("SCOPE\tSUBNET\tUSED\tUTILISATION", rows)
	return nil
}

func deref[T ~string](p *T) string {
	if p == nil {
		return ""
	}
	return string(*p)
}

// ---- scopes ------------------------------------------------------------------------

func (c *ctl) scopes(ctx context.Context) ([]gen.Scope, error) {
	r, err := c.c.ListScopesWithResponse(ctx)
	if err := check(r, err); err != nil {
		return nil, err
	}
	return *r.JSON200, nil
}

// findScope resolves an id or name.
func (c *ctl) findScope(ctx context.Context, ref string) (gen.Scope, error) {
	all, err := c.scopes(ctx)
	if err != nil {
		return gen.Scope{}, err
	}
	id, _ := strconv.ParseInt(ref, 10, 64)
	for _, s := range all {
		if s.Id == id || strings.EqualFold(s.Name, ref) {
			return s, nil
		}
	}
	return gen.Scope{}, fmt.Errorf("no scope %q", ref)
}

func (c *ctl) scopesList(ctx context.Context) error {
	all, err := c.scopes(ctx)
	if err != nil {
		return err
	}
	if c.json {
		return c.printJSON(all)
	}
	rows := make([][]string, 0, len(all))
	for _, s := range all {
		rows = append(rows, []string{strconv.FormatInt(s.Id, 10), s.Name, s.Subnet, s.Start + " - " + s.End, duration(s.LeaseTime), yesNo(s.Enabled)})
	}
	c.table("ID\tNAME\tSUBNET\tRANGE\tLEASE\tENABLED", rows)
	return nil
}

func (c *ctl) scopesShow(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return usageError("scopes show needs a scope id or name")
	}
	s, err := c.findScope(ctx, args[0])
	if err != nil {
		return err
	}
	st, err := c.c.GetScopeStatsWithResponse(ctx, s.Id)
	if err := check(st, err); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(map[string]any{"scope": s, "stats": st.JSON200})
	}
	x := st.JSON200
	fmt.Fprintf(c.out, "Name:         %s (id %d)\n", s.Name, s.Id)
	fmt.Fprintf(c.out, "Description:  %s\n", s.Description)
	fmt.Fprintf(c.out, "Subnet:       %s\n", s.Subnet)
	fmt.Fprintf(c.out, "Range:        %s - %s\n", s.Start, s.End)
	fmt.Fprintf(c.out, "Lease time:   %s\n", duration(s.LeaseTime))
	fmt.Fprintf(c.out, "Enabled:      %s, BOOTP: %s\n", yesNo(s.Enabled), yesNo(s.Bootp))
	fmt.Fprintf(c.out, "Addresses:    %d usable, %d in use, %d reserved, %d available (%.1f %%)\n",
		x.Usable, x.InUse, x.Reserved, x.Available, x.UtilizationPercent)
	return nil
}

func (c *ctl) scopesExport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("scopes export", flag.ContinueOnError)
	file := fs.String("f", "", "output file (default stdout)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("scopes export needs a scope id or name")
	}
	s, err := c.findScope(ctx, pos[0])
	if err != nil {
		return err
	}
	r, err := c.c.ExportScopeWithResponse(ctx, s.Id)
	if err := check(r, err); err != nil {
		return err
	}
	return c.writeOut(*file, r.Body)
}

func (c *ctl) scopesImport(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return usageError("scopes import needs a bundle file")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	r, err := c.c.ImportScopeWithBodyWithResponse(ctx, "application/json", bytes.NewReader(data))
	if err := check(r, err); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "scope %q created (id %d)\n", r.JSON201.Name, r.JSON201.Id)
	return nil
}

// ---- leases ------------------------------------------------------------------------

func (c *ctl) leasesList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("leases list", flag.ContinueOnError)
	scopeRef := fs.String("scope", "", "scope id or name")
	state := fs.String("state", "", "active, expired, released or declined")
	q := fs.String("q", "", "search IP, MAC, client id, hostname")
	limit := fs.Int("limit", 1000, "maximum number of leases")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	params := &gen.ListLeasesParams{Limit: limit}
	if *scopeRef != "" {
		s, err := c.findScope(ctx, *scopeRef)
		if err != nil {
			return err
		}
		params.ScopeId = &s.Id
	}
	if *state != "" {
		st := gen.LeaseState(*state)
		params.State = &st
	}
	if *q != "" {
		params.Q = q
	}
	r, err := c.c.ListLeasesWithResponse(ctx, params)
	if err := check(r, err); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(r.JSON200)
	}
	rows := make([][]string, 0, len(r.JSON200.Items))
	for _, l := range r.JSON200.Items {
		exp := l.ExpiresAt.Local().Format("2006-01-02 15:04:05")
		if l.ExpiresAt.Year() >= 9999 {
			exp = "never"
		}
		rows = append(rows, []string{l.Ip, l.Mac, l.Hostname, l.ScopeName, string(l.State), exp})
	}
	c.table("IP\tMAC\tHOSTNAME\tSCOPE\tSTATE\tEXPIRES", rows)
	if r.JSON200.Total > len(r.JSON200.Items) {
		fmt.Fprintf(c.errOut, "%d of %d leases shown (--limit)\n", len(r.JSON200.Items), r.JSON200.Total)
	}
	return nil
}

func (c *ctl) leasesShow(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return usageError("leases show needs an IP address")
	}
	r, err := c.c.GetLeaseWithResponse(ctx, args[0])
	if err := check(r, err); err != nil {
		return err
	}
	return c.printJSON(r.JSON200)
}

func (c *ctl) leasesDelete(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return usageError("leases delete needs an IP address")
	}
	r, err := c.c.DeleteLeaseWithResponse(ctx, args[0])
	if err := check(r, err); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "lease %s deleted\n", args[0])
	return nil
}

func (c *ctl) leasesExport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("leases export", flag.ContinueOnError)
	file := fs.String("f", "", "output file (default stdout)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	r, err := c.c.ExportLeasesWithResponse(ctx)
	if err := check(r, err); err != nil {
		return err
	}
	return c.writeOut(*file, r.Body)
}

func (c *ctl) leasesImport(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return usageError("leases import needs an export file")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	r, err := c.c.ImportLeasesWithBodyWithResponse(ctx, "application/json", bytes.NewReader(data))
	if err := check(r, err); err != nil {
		return err
	}
	x := r.JSON200
	fmt.Fprintf(c.out, "%d records read, %d changed, %d conflicts resolved\n", x.Records, x.Changed, x.Conflicts)
	return nil
}

// ---- HA ---------------------------------------------------------------------------

func (c *ctl) haStatus(ctx context.Context) error {
	r, err := c.c.GetHAStatusWithResponse(ctx)
	if err := check(r, err); err != nil {
		return err
	}
	h := r.JSON200
	if c.json {
		return c.printJSON(h)
	}
	if !h.Enabled {
		fmt.Fprintln(c.out, "high availability is not configured")
		return nil
	}
	conn := false
	if h.Connected != nil {
		conn = *h.Connected
	}
	fmt.Fprintf(c.out, "State:          %s\n", deref(h.State))
	fmt.Fprintf(c.out, "This node:      %s (%s, %s)\n", deref(h.NodeId), deref(h.Role), deref(h.Mode))
	fmt.Fprintf(c.out, "Partner:        %s at %s, connected: %s, state: %s\n", deref(h.PeerId), deref(h.PeerAddress), yesNo(conn), deref(h.PeerState))
	if h.LastContact != nil {
		fmt.Fprintf(c.out, "Last contact:   %s\n", h.LastContact.Local().Format(time.RFC3339))
	}
	if h.TakeoverAt != nil {
		fmt.Fprintf(c.out, "Takeover at:    %s\n", h.TakeoverAt.Local().Format(time.RFC3339))
	}
	if h.ConfigLeader != nil {
		fmt.Fprintf(c.out, "Config leader:  %s\n", yesNo(*h.ConfigLeader))
	}
	for _, e := range []*string{h.Error, h.ResyncError, h.ConfigError} {
		if e != nil && *e != "" {
			fmt.Fprintf(c.out, "Error:          %s\n", *e)
		}
	}
	return nil
}

func (c *ctl) partnerDown(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ha partner-down", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "confirm")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if !*yes {
		return usageError("ha partner-down takes over the partner's addresses after MCLT; confirm with --yes only if the partner is really down")
	}
	r, err := c.c.DeclarePartnerDownWithResponse(ctx)
	if err := check(r, err); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "PARTNER_DOWN active; whole pool from %s\n", r.JSON200.TakeoverAt.Local().Format(time.RFC3339))
	return nil
}

// ---- config ------------------------------------------------------------------------

func (c *ctl) configExport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("config export", flag.ContinueOnError)
	format := fs.String("format", "yaml", "yaml or json")
	file := fs.String("f", "", "output file (default stdout)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	f := gen.ExportConfigParamsFormat(*format)
	r, err := c.c.ExportConfigWithResponse(ctx, &gen.ExportConfigParams{Format: &f})
	if err := check(r, err); err != nil {
		return err
	}
	return c.writeOut(*file, r.Body)
}

func (c *ctl) configImport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("config import", flag.ContinueOnError)
	mode := fs.String("mode", "merge", "merge or replace")
	dry := fs.Bool("dry-run", false, "only validate")
	netsh := fs.Bool("netsh", false, "the file is the output of 'netsh dhcp server dump'")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("config import needs a file")
	}
	data, err := os.ReadFile(pos[0])
	if err != nil {
		return err
	}
	var sum *gen.ConfigImportResult
	if *netsh {
		m := gen.ImportNetshParamsMode(*mode)
		r, err := c.c.ImportNetshWithBodyWithResponse(ctx, &gen.ImportNetshParams{Mode: &m, DryRun: dry}, "text/plain", bytes.NewReader(data))
		if err := check(r, err); err != nil {
			return err
		}
		sum = &r.JSON200.Result
		for _, w := range r.JSON200.Warnings {
			if w.Line > 0 {
				fmt.Fprintf(c.errOut, "line %d: %s\n", w.Line, w.Reason)
			} else {
				fmt.Fprintf(c.errOut, "%s: %s\n", w.Text, w.Reason)
			}
		}
	} else {
		ct := "application/yaml"
		if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '{' {
			ct = "application/json"
		}
		m := gen.ImportConfigParamsMode(*mode)
		r, err := c.c.ImportConfigWithBodyWithResponse(ctx, &gen.ImportConfigParams{Mode: &m, DryRun: dry}, ct, bytes.NewReader(data))
		if err := check(r, err); err != nil {
			return err
		}
		sum = r.JSON200
	}
	if c.json {
		return c.printJSON(sum)
	}
	verb := "imported"
	if sum.DryRun {
		verb = "valid (dry run, nothing changed)"
	}
	fmt.Fprintf(c.out, "%s (%s): %d scopes, %d reservations, %d policies, %d classes, %d option definitions, %d options, %d MAC filters, %d superscopes, %d DNS zones, %d IPv6 scopes\n",
		verb, sum.Mode, sum.Scopes, sum.Reservations, sum.Policies, sum.Classes, sum.OptionDefinitions, sum.Options, sum.MacFilters, sum.Superscopes, sum.DnsZones, sum.Scopes6)
	return nil
}

// ---- backup / restore -----------------------------------------------------------------

func (c *ctl) backup(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return usageError("backup needs an output file")
	}
	r, err := c.c.DownloadBackupWithResponse(ctx)
	if err := check(r, err); err != nil {
		return err
	}
	f, err := os.OpenFile(args[0], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(r.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "backup written to %s (%d bytes)\n", args[0], len(r.Body))
	return nil
}

func (c *ctl) restore(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "confirm")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("restore needs a backup file")
	}
	if !*yes {
		return usageError("restore replaces configuration, users and leases and ends all sessions; confirm with --yes")
	}
	data, err := os.ReadFile(pos[0])
	if err != nil {
		return err
	}
	r, err := c.c.RestoreBackupWithBodyWithResponse(ctx, "application/octet-stream", bytes.NewReader(data))
	if err := check(r, err); err != nil {
		return err
	}
	x := r.JSON200
	fmt.Fprintf(c.out, "restored %d scopes, %d reservations, %d users, %d leases (backup schema %d)\n", x.Scopes, x.Reservations, x.Users, x.Leases, x.SchemaVersion)
	return nil
}

func (c *ctl) dnsZones(ctx context.Context) error {
	r, err := c.c.ListDnsZonesWithResponse(ctx)
	if err := check(r, err); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(r.JSON200)
	}
	rows := make([][]string, 0, len(*r.JSON200))
	for _, z := range *r.JSON200 {
		key := z.KeyName
		if key == "" {
			key = "(unsigned)"
		}
		rows = append(rows, []string{z.Name, z.Server, key})
	}
	c.table("ZONE\tSERVER\tTSIG KEY", rows)
	return nil
}

func (c *ctl) dnsRegistrations(ctx context.Context) error {
	r, err := c.c.ListDnsRegistrationsWithResponse(ctx)
	if err := check(r, err); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(r.JSON200)
	}
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	rows := make([][]string, 0, len(*r.JSON200))
	for _, g := range *r.JSON200 {
		rows = append(rows, []string{g.Ip, dash(g.Fqdn), dash(g.ARecord), dash(g.PtrRecord), string(g.State), g.LastError})
	}
	c.table("IP\tNAME\tA\tPTR\tSTATE\tNOTE", rows)
	return nil
}

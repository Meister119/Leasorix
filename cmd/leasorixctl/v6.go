package main

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/meister119/leasorix/internal/api/gen"
)

// ---- DHCPv6 ------------------------------------------------------------------------

func (c *ctl) v6(ctx context.Context, args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch {
	case sub == "scopes" || sub == "":
		return c.v6Scopes(ctx)
	case sub == "leases" && len(args) > 1 && args[1] == "delete":
		return c.v6LeaseDelete(ctx, args[2:])
	case sub == "leases":
		return c.v6Leases(ctx, args[1:])
	}
	return usageError(fmt.Sprintf("unknown command %q", "v6 "+strings.Join(args, " ")))
}

func (c *ctl) scopes6(ctx context.Context) ([]gen.Scope6, error) {
	r, err := c.c.ListScopes6WithResponse(ctx)
	if err := check(r, err); err != nil {
		return nil, err
	}
	return *r.JSON200, nil
}

func (c *ctl) v6Scopes(ctx context.Context) error {
	all, err := c.scopes6(ctx)
	if err != nil {
		return err
	}
	st, err := c.c.GetStats6WithResponse(ctx)
	if err := check(st, err); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(map[string]any{"scopes": all, "stats": st.JSON200})
	}
	stats := make(map[int64]gen.Scope6Stats, len(*st.JSON200))
	for _, s := range *st.JSON200 {
		stats[s.ScopeId] = s
	}
	rows := make([][]string, 0, len(all))
	for _, s := range all {
		rng, pd := "-", "-"
		if s.Start != "" {
			rng = s.Start + " - " + s.End
		}
		if s.PdPool != "" {
			pd = fmt.Sprintf("%s /%d", s.PdPool, s.PdLength)
		}
		x := stats[s.Id]
		rows = append(rows, []string{strconv.FormatInt(s.Id, 10), s.Name, s.Prefix, rng, pd,
			duration(s.PreferredLifetime) + "/" + duration(s.ValidLifetime), yesNo(s.Enabled),
			strconv.Itoa(x.AddressesInUse), strconv.Itoa(x.PrefixesInUse)})
	}
	c.table("ID\tNAME\tPREFIX\tRANGE\tPD POOL\tPREFERRED/VALID\tENABLED\tADDRESSES\tPREFIXES", rows)
	return nil
}

func (c *ctl) v6Leases(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("v6 leases", flag.ContinueOnError)
	scope := fs.String("scope", "", "scope id or name")
	q := fs.String("q", "", "search address, DUID or host name")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 || (len(pos) == 1 && pos[0] != "list") {
		return usageError("v6 leases takes only flags")
	}
	var p gen.ListLeases6Params
	names := map[int64]string{}
	all, err := c.scopes6(ctx)
	if err != nil {
		return err
	}
	for _, s := range all {
		names[s.Id] = s.Name
		if *scope != "" && (strconv.FormatInt(s.Id, 10) == *scope || strings.EqualFold(s.Name, *scope)) {
			id := s.Id
			p.ScopeId = &id
		}
	}
	if *scope != "" && p.ScopeId == nil {
		return fmt.Errorf("no IPv6 scope %q", *scope)
	}
	if *q != "" {
		p.Q = q
	}
	r, err := c.c.ListLeases6WithResponse(ctx, &p)
	if err := check(r, err); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(r.JSON200)
	}
	rows := make([][]string, 0, len(*r.JSON200))
	for _, l := range *r.JSON200 {
		addr := l.Address
		if l.Kind == gen.Lease6KindPd {
			addr = fmt.Sprintf("%s/%d", l.Address, l.PrefixLen)
		}
		host := l.Hostname
		if host == "" {
			host = "-"
		}
		rows = append(rows, []string{addr, string(l.Kind), l.Duid, strconv.FormatInt(l.Iaid, 10), host, names[l.ScopeId],
			string(l.State), l.ExpiresAt.Local().Format("2006-01-02 15:04:05")})
	}
	c.table("ADDRESS\tKIND\tDUID\tIAID\tHOSTNAME\tSCOPE\tSTATE\tEXPIRES", rows)
	return nil
}

func (c *ctl) v6LeaseDelete(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return usageError("v6 leases delete needs an address")
	}
	// A delegated prefix may be given with its length.
	addr, _, _ := strings.Cut(args[0], "/")
	r, err := c.c.DeleteLease6WithResponse(ctx, addr)
	if err := check(r, err); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "lease %s deleted\n", args[0])
	return nil
}

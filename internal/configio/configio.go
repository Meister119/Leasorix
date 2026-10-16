// Package configio exports and imports the complete DHCP configuration as a
// portable YAML or JSON document (format leasorix-config/v1) and converts
// Windows "netsh dhcp server dump" output into such a document.
//
// Imports run against a temporary database through the regular store
// methods, so every validation rule applies; only a fully successful result
// is copied into the live database (in one transaction). Users and API
// tokens are not part of the document.
package configio

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/dhcpopt"
	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/internal/store"
)

// Format identifies the document format.
const Format = "leasorix-config/v1"

// Document is the portable configuration.
type Document struct {
	Format            string               `json:"format" yaml:"format"`
	ExportedAt        *time.Time           `json:"exported_at,omitempty" yaml:"exported_at,omitempty"`
	Settings          *Settings            `json:"settings,omitempty" yaml:"settings,omitempty"`
	OptionDefinitions []OptionDefinition   `json:"option_definitions,omitempty" yaml:"option_definitions,omitempty"`
	Classes           []Class              `json:"classes,omitempty" yaml:"classes,omitempty"`
	ServerOptions     []scope.BundleOption `json:"server_options,omitempty" yaml:"server_options,omitempty"`
	ServerPolicies    []scope.BundlePolicy `json:"server_policies,omitempty" yaml:"server_policies,omitempty"`
	MACFilters        []MACFilter          `json:"mac_filters,omitempty" yaml:"mac_filters,omitempty"`
	Superscopes       []Superscope         `json:"superscopes,omitempty" yaml:"superscopes,omitempty"`
	Scopes            []scope.Bundle       `json:"scopes,omitempty" yaml:"scopes,omitempty"`
}

// Settings are the server wide switches.
type Settings struct {
	MACFilterAllowEnabled bool `json:"mac_filter_allow_enabled" yaml:"mac_filter_allow_enabled"`
	MACFilterDenyEnabled  bool `json:"mac_filter_deny_enabled" yaml:"mac_filter_deny_enabled"`
}

// OptionDefinition is a user defined option code.
type OptionDefinition struct {
	Code        uint8  `json:"code" yaml:"code"`
	Name        string `json:"name" yaml:"name"`
	Type        string `json:"type" yaml:"type"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// Class is a vendor or user class.
type Class struct {
	Name        string `json:"name" yaml:"name"`
	Type        string `json:"type" yaml:"type"`
	Data        string `json:"data" yaml:"data"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// MACFilter is an allow or deny list entry.
type MACFilter struct {
	List        string `json:"list" yaml:"list"`
	Pattern     string `json:"pattern" yaml:"pattern"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// Superscope groups scopes (membership is given in the scopes).
type Superscope struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// Export builds the document of the current configuration.
func Export(ctx context.Context, s *scope.Store, now time.Time) (*Document, error) {
	at := now.UTC().Truncate(time.Second)
	d := &Document{Format: Format, ExportedAt: &at}
	fs, err := s.FilterSettings(ctx)
	if err != nil {
		return nil, err
	}
	d.Settings = &Settings{MACFilterAllowEnabled: fs.AllowEnabled, MACFilterDenyEnabled: fs.DenyEnabled}
	defs, err := s.CustomOptionDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	for _, def := range defs {
		d.OptionDefinitions = append(d.OptionDefinitions, OptionDefinition{Code: def.Code, Name: def.Name, Type: string(def.Type), Description: def.Description})
	}
	classes, err := s.ListClasses(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range classes {
		d.Classes = append(d.Classes, Class{Name: c.Name, Type: string(c.Type), Data: c.Data, Description: c.Description})
	}
	opts, err := s.ListOptions(ctx, scope.ServerOwner)
	if err != nil {
		return nil, err
	}
	for _, o := range opts {
		d.ServerOptions = append(d.ServerOptions, scope.BundleOption{Code: o.Code, Value: o.Value})
	}
	pols, err := s.ListPolicies(ctx, 0)
	if err != nil {
		return nil, err
	}
	for _, p := range pols {
		bp, err := s.PolicyBundle(ctx, p)
		if err != nil {
			return nil, err
		}
		d.ServerPolicies = append(d.ServerPolicies, bp)
	}
	filters, err := s.ListFilters(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, f := range filters {
		d.MACFilters = append(d.MACFilters, MACFilter{List: f.List, Pattern: f.Pattern, Description: f.Description})
	}
	sss, err := s.ListSuperscopes(ctx)
	if err != nil {
		return nil, err
	}
	for _, ss := range sss {
		d.Superscopes = append(d.Superscopes, Superscope{Name: ss.Name, Description: ss.Description})
	}
	scopes, err := s.ListScopes(ctx)
	if err != nil {
		return nil, err
	}
	for _, sc := range scopes {
		b, err := s.ExportScope(ctx, sc.ID)
		if err != nil {
			return nil, err
		}
		b.Format = ""
		d.Scopes = append(d.Scopes, *b)
	}
	return d, nil
}

// Encode writes the document as YAML or JSON.
func Encode(w io.Writer, d *Document, asYAML bool) error {
	if asYAML {
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		if err := enc.Encode(d); err != nil {
			return err
		}
		return enc.Close()
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(d)
}

// Decode parses a YAML or JSON document (JSON is valid YAML, but JSON is
// decoded strictly with the JSON decoder when it starts with '{').
func Decode(data []byte) (*Document, error) {
	var d Document
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&d); err != nil {
			return nil, invalid("invalid JSON: %v", err)
		}
	} else {
		dec := yaml.NewDecoder(bytes.NewReader(trimmed))
		dec.KnownFields(true)
		if err := dec.Decode(&d); err != nil && !errors.Is(err, io.EOF) {
			return nil, invalid("invalid YAML: %v", err)
		}
	}
	if d.Format != Format {
		return nil, invalid("unsupported format %q (expected %s)", d.Format, Format)
	}
	return &d, nil
}

func invalid(format string, args ...any) error {
	var v apperr.Validator
	v.Add("document", format, args...)
	return v.Err()
}

// Mode selects how an import combines with the existing configuration.
type Mode string

// Import modes.
const (
	// ModeMerge adds the document to the existing configuration. Objects
	// that already exist (same scope name/subnet, class name, ...) are
	// errors; server options and settings from the document win.
	ModeMerge Mode = "merge"
	// ModeReplace replaces the whole DHCP configuration. Leases are kept
	// for addresses that are still inside a scope.
	ModeReplace Mode = "replace"
)

// Options control an import.
type Options struct {
	Mode Mode
	// DryRun validates the document against the configuration without
	// changing anything.
	DryRun bool
}

// Summary counts the imported objects.
type Summary struct {
	Mode              Mode `json:"mode"`
	DryRun            bool `json:"dry_run"`
	Scopes            int  `json:"scopes"`
	Reservations      int  `json:"reservations"`
	Policies          int  `json:"policies"`
	Classes           int  `json:"classes"`
	OptionDefinitions int  `json:"option_definitions"`
	Options           int  `json:"options"`
	MACFilters        int  `json:"mac_filters"`
	Superscopes       int  `json:"superscopes"`
}

// importTables are the configuration tables an import replaces (users and
// API tokens stay untouched).
var importTables = store.DHCPConfigTables

// Import applies a document to the live database and reloads the scope
// store. The context actor is recorded in one audit entry.
func Import(ctx context.Context, db *store.DB, live *scope.Store, d *Document, opt Options) (*Summary, error) {
	if opt.Mode != ModeMerge && opt.Mode != ModeReplace {
		return nil, invalid("mode must be merge or replace")
	}
	if d.Format != Format {
		return nil, invalid("unsupported format %q", d.Format)
	}
	dir, err := os.MkdirTemp(filepath.Dir(db.Path()), ".import-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	tmp, err := store.Open(ctx, filepath.Join(dir, store.FileName))
	if err != nil {
		return nil, err
	}
	defer tmp.Close()
	if err := tmp.Migrate(ctx, slog.New(slog.DiscardHandler)); err != nil {
		return nil, err
	}
	if opt.Mode == ModeMerge {
		cur, err := store.DumpTables(ctx, db.R, importTables)
		if err != nil {
			return nil, err
		}
		if err := tmp.ReplaceTables(ctx, importTables, cur, store.ReplaceOptions{}); err != nil {
			return nil, err
		}
	}
	ts := scope.NewStore(tmp)
	if err := ts.Load(ctx); err != nil {
		return nil, err
	}
	sum, err := apply(ctx, ts, d)
	if err != nil {
		return nil, err
	}
	sum.Mode, sum.DryRun = opt.Mode, opt.DryRun
	if opt.DryRun {
		return sum, nil
	}
	result, err := store.DumpTables(ctx, tmp.R, importTables)
	if err != nil {
		return nil, err
	}
	entry := audit.New(ctx, "config.import", "config", string(opt.Mode), map[string]any{
		"mode": string(opt.Mode), "scopes": sum.Scopes, "reservations": sum.Reservations, "policies": sum.Policies,
		"classes": sum.Classes, "options": sum.Options, "mac_filters": sum.MACFilters,
	})
	err = db.ReplaceTables(ctx, importTables, result, store.ReplaceOptions{
		After: func(ctx context.Context, tx *sql.Tx) error {
			if err := store.RemapLeases(ctx, tx); err != nil {
				return err
			}
			return audit.Insert(ctx, tx, entry)
		},
	})
	if err != nil {
		return nil, err
	}
	return sum, live.Load(ctx)
}

// apply creates the document's objects through the store (all validation
// rules apply). Errors name the failing object.
func apply(ctx context.Context, s *scope.Store, d *Document) (*Summary, error) {
	sum := &Summary{}
	wrap := func(what string, err error) error {
		var ve *apperr.ValidationError
		if errors.As(err, &ve) {
			var v apperr.Validator
			v.Add("document", "%s: %s", what, err.Error())
			return v.Err()
		}
		if errors.Is(err, apperr.ErrConflict) || errors.Is(err, apperr.ErrNotFound) {
			return fmt.Errorf("%s: %w", what, err)
		}
		// Remaining store errors are constraint violations of the document.
		return apperr.Conflictf("%s: %v", what, err)
	}
	if d.Settings != nil {
		if err := s.SetFilterSettings(ctx, scope.FilterSettings{AllowEnabled: d.Settings.MACFilterAllowEnabled, DenyEnabled: d.Settings.MACFilterDenyEnabled}); err != nil {
			return nil, wrap("settings", err)
		}
	}
	for _, od := range d.OptionDefinitions {
		if _, err := s.CreateOptionDefinition(ctx, dhcpopt.Definition{Code: od.Code, Name: od.Name, Type: dhcpopt.Type(od.Type), Description: od.Description}); err != nil {
			return nil, wrap(fmt.Sprintf("option definition %d", od.Code), err)
		}
		sum.OptionDefinitions++
	}
	for _, c := range d.Classes {
		if _, err := s.CreateClass(ctx, scope.Class{Name: c.Name, Type: scope.ClassType(c.Type), Data: c.Data, Description: c.Description}); err != nil {
			return nil, wrap(fmt.Sprintf("class %q", c.Name), err)
		}
		sum.Classes++
	}
	for _, o := range d.ServerOptions {
		if _, err := s.SetOption(ctx, scope.OptionValue{Owner: scope.ServerOwner, Code: o.Code, Value: o.Value}); err != nil {
			return nil, wrap(fmt.Sprintf("server option %d", o.Code), err)
		}
		sum.Options++
	}
	for _, bp := range d.ServerPolicies {
		if _, err := s.ImportPolicy(ctx, 0, bp); err != nil {
			return nil, wrap("server policy", err)
		}
		sum.Policies++
		sum.Options += len(bp.Options)
	}
	for _, f := range d.MACFilters {
		if _, err := s.AddFilter(ctx, scope.MACFilter{List: f.List, Pattern: f.Pattern, Description: f.Description}); err != nil {
			if errors.Is(err, apperr.ErrConflict) {
				continue // already present
			}
			return nil, wrap(fmt.Sprintf("MAC filter %s", f.Pattern), err)
		}
		sum.MACFilters++
	}
	existing, err := s.ListSuperscopes(ctx)
	if err != nil {
		return nil, err
	}
	for _, ss := range d.Superscopes {
		found := false
		for _, e := range existing {
			if strings.EqualFold(e.Name, ss.Name) {
				found = true
			}
		}
		if found {
			continue
		}
		if _, err := s.CreateSuperscope(ctx, scope.Superscope{Name: ss.Name, Description: ss.Description}); err != nil {
			return nil, wrap(fmt.Sprintf("superscope %q", ss.Name), err)
		}
		sum.Superscopes++
	}
	for i := range d.Scopes {
		b := d.Scopes[i]
		b.Format = scope.BundleFormat
		if _, err := s.ImportScope(ctx, &b); err != nil {
			return nil, wrap(fmt.Sprintf("scope %q", b.Scope.Name), err)
		}
		sum.Scopes++
		sum.Reservations += len(b.Reservations)
		sum.Policies += len(b.Policies)
		sum.Options += len(b.Options)
		for _, r := range b.Reservations {
			sum.Options += len(r.Options)
		}
		for _, p := range b.Policies {
			sum.Options += len(p.Options)
		}
	}
	return sum, nil
}

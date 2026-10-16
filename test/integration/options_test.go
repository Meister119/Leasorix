//go:build integration && linux

package integration

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"

	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/test/dhcpclient"
)

func setOpt(t *testing.T, s *scope.Store, o scope.Owner, code uint8, v string) {
	t.Helper()
	if _, err := s.SetOption(context.Background(), scope.OptionValue{Owner: o, Code: code, Value: v}); err != nil {
		t.Fatal(err)
	}
}

// seedPXE creates a scope with standard options and a PXE policy.
func seedPXE(t *testing.T, tp *topo) {
	tp.seed(func(ctx context.Context, s *scope.Store) {
		sc := mustScope(t, s, "lan", "10.77.0.0/16", "10.77.0.100", "10.77.0.200", time.Hour)
		so := scope.Owner{Kind: scope.OwnerScope, ID: sc.ID}
		setOpt(t, s, scope.ServerOwner, 6, "10.77.0.53, 10.77.0.54")
		setOpt(t, s, so, 3, "10.77.0.1")
		setOpt(t, s, so, 15, "lan.example")
		setOpt(t, s, so, 42, "10.77.0.123")
		if _, err := s.CreateClass(ctx, scope.Class{Name: "PXE", Type: scope.ClassVendor, Data: "PXEClient*"}); err != nil {
			t.Fatal(err)
		}
		pol, err := s.CreatePolicy(ctx, scope.Policy{ScopeID: sc.ID, Name: "pxe-boot", Enabled: true,
			Conditions: []scope.Condition{{Field: scope.FieldClass, Value: "PXE"}}})
		if err != nil {
			t.Fatal(err)
		}
		po := scope.Owner{Kind: scope.OwnerPolicy, ID: pol.ID}
		setOpt(t, s, po, 66, "10.77.0.5")
		setOpt(t, s, po, 67, "pxelinux.0")
	})
}

// TestStandardOptionsUdhcpc: router, DNS, domain and NTP reach a real client.
func TestStandardOptionsUdhcpc(t *testing.T) {
	if _, err := exec.LookPath("udhcpc"); err != nil {
		t.Skip("udhcpc not installed")
	}
	tp := newTopo(t)
	seedPXE(t, tp)
	tp.startServer("")
	events := filepath.Join(tp.dir, "udhcpc.events")
	script := filepath.Join(tp.dir, "udhcpc-script")
	writeScript(t, script, fmt.Sprintf("[ \"$1\" = bound ] && echo \"ip=$ip router=$router dns=$dns domain=$domain ntpsrv=$ntpsrv bootfile=$bootfile boot_file=$boot_file\" >> %s\nexit 0\n", events))
	out, err := tp.cli.Output("udhcpc", "-i", "c0", "-f", "-q", "-n", "-t", "5", "-T", "1", "-s", script, "-O", "ntpsrv")
	if err != nil {
		t.Fatalf("udhcpc: %v\n%s", err, out)
	}
	lines := readLines(events)
	if len(lines) != 1 {
		t.Fatalf("events %v\n%s", lines, out)
	}
	want := "ip=10.77.0.100 router=10.77.0.1 dns=10.77.0.53 10.77.0.54 domain=lan.example ntpsrv=10.77.0.123 bootfile= boot_file="
	if lines[0] != want {
		t.Fatalf("got  %q\nwant %q", lines[0], want)
	}
}

// TestPXEClientGetsBootOptions: a client with vendor class PXEClient gets
// options 66/67 and the BOOTP header fields; tested with udhcpc and with
// the Go client imitating a PXE ROM.
func TestPXEClientGetsBootOptions(t *testing.T) {
	tp := newTopo(t)
	seedPXE(t, tp)
	tp.startServer("")

	if _, err := exec.LookPath("udhcpc"); err == nil {
		events := filepath.Join(tp.dir, "udhcpc.events")
		script := filepath.Join(tp.dir, "udhcpc-script")
		writeScript(t, script, fmt.Sprintf("[ \"$1\" = bound ] && echo \"tftp=$tftp bootfile=$bootfile siaddr=$siaddr boot_file=$boot_file\" >> %s\nexit 0\n", events))
		out, err := tp.cli.Output("udhcpc", "-i", "c0", "-f", "-q", "-n", "-t", "5", "-T", "1", "-s", script,
			"-V", "PXEClient:Arch:00000:UNDI:002001", "-O", "tftp", "-O", "bootfile")
		if err != nil {
			t.Fatalf("udhcpc: %v\n%s", err, out)
		}
		lines := readLines(events)
		want := "tftp=10.77.0.5 bootfile=pxelinux.0 siaddr=10.77.0.5 boot_file=pxelinux.0"
		if len(lines) != 1 || lines[0] != want {
			t.Fatalf("udhcpc PXE: got %v, want %q\n%s", lines, want, out)
		}
	}

	conn := tp.conn()
	var l dhcpv4.OptionCodeList
	for _, c := range []uint8{1, 2, 3, 4, 5, 6, 12, 13, 15, 17, 18, 22, 23, 28, 40, 41, 42, 43, 50, 51, 54, 58, 59, 60, 66, 67, 97, 128, 129, 130, 131, 132, 133, 134, 135} {
		l.Add(dhcpv4.GenericOptionCode(c))
	}
	pxe := &dhcpclient.Client{Conn: conn, MAC: dhcpclient.RandomMAC(), Options: []dhcpv4.Option{
		dhcpv4.OptClassIdentifier("PXEClient:Arch:00007:UNDI:003016"),
		dhcpv4.OptGeneric(dhcpv4.OptionClientSystemArchitectureType, []byte{0, 7}),
		dhcpv4.OptGeneric(dhcpv4.OptionClientMachineIdentifier, append([]byte{0}, make([]byte, 16)...)),
		dhcpv4.OptParameterRequestList(l...),
		dhcpv4.OptMaxMessageSize(1260),
	}}
	ack, err := pxe.DORA(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ack.TFTPServerName() != "10.77.0.5" || ack.BootFileNameOption() != "pxelinux.0" {
		t.Fatalf("PXE options missing: %s", ack.Summary())
	}
	if ack.BootFileName != "pxelinux.0" || ack.ServerIPAddr.String() != "10.77.0.5" {
		t.Fatalf("BOOTP header: file=%q siaddr=%s", ack.BootFileName, ack.ServerIPAddr)
	}
	// A regular client in the same scope does not get boot options.
	plain := &dhcpclient.Client{Conn: conn, MAC: dhcpclient.RandomMAC()}
	ack, err = plain.DORA(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ack.Options.Has(dhcpv4.OptionBootfileName) || ack.BootFileName != "" {
		t.Fatalf("boot options leaked to non-PXE client: %s", ack.Summary())
	}
}

// TestDeniedMACGetsNoResponse: a MAC on the deny list is ignored, others are
// served.
func TestDeniedMACGetsNoResponse(t *testing.T) {
	tp := newTopo(t)
	blocked := dhcpclient.RandomMAC()
	tp.seed(func(ctx context.Context, s *scope.Store) {
		mustScope(t, s, "lan", "10.77.0.0/16", "10.77.0.100", "10.77.0.200", time.Hour)
		if _, err := s.AddFilter(ctx, scope.MACFilter{List: scope.FilterDeny, Pattern: blocked.String()}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetFilterSettings(ctx, scope.FilterSettings{DenyEnabled: true}); err != nil {
			t.Fatal(err)
		}
	})
	tp.startServer("")
	conn := tp.conn()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := &dhcpclient.Client{Conn: conn, MAC: blocked}
	if _, err := c.Discover(ctx); !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, dhcpclient.ErrTimeout) {
		t.Fatalf("blocked MAC got an answer (err=%v)", err)
	}
	ok := &dhcpclient.Client{Conn: conn, MAC: dhcpclient.RandomMAC()}
	if _, err := ok.DORA(context.Background()); err != nil {
		t.Fatalf("unblocked client: %v", err)
	}
	if b, err := readLog(tp); err == nil && !strings.Contains(b, "client filtered") {
		t.Error("filter decision not logged")
	}
}

func readLog(tp *topo) (string, error) {
	lines := readLines(tp.logPath)
	if lines == nil {
		return "", errors.New("no log")
	}
	return strings.Join(lines, "\n"), nil
}

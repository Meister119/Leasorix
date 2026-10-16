package dhcp6

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/iana"

	"github.com/meister119/leasorix/internal/store"
)

const duidKey = "dhcp6.server_duid"

// duidEpoch is the DUID-LLT time base (2000-01-01 UTC).
var duidEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// ServerDUID returns this server's DUID. It is created once (DUID-LLT from
// the first configured interface with an Ethernet address, otherwise a
// DUID-LL with a random address) and kept in local_state, so it survives
// restarts and hardware changes. HA partners have different DUIDs.
func ServerDUID(ctx context.Context, db *store.DB, ifaces []string, now time.Time) (dhcpv6.DUID, error) {
	var v string
	err := db.R.QueryRowContext(ctx, `SELECT value FROM local_state WHERE key = ?`, duidKey).Scan(&v)
	if err == nil {
		b, err := hex.DecodeString(v)
		if err != nil {
			return nil, err
		}
		return dhcpv6.DUIDFromBytes(b)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var duid dhcpv6.DUID
	for _, name := range ifaces {
		ifi, err := net.InterfaceByName(name)
		if err == nil && len(ifi.HardwareAddr) == 6 {
			duid = &dhcpv6.DUIDLLT{HWType: iana.HWTypeEthernet, Time: uint32(now.Sub(duidEpoch) / time.Second), LinkLayerAddr: ifi.HardwareAddr}
			break
		}
	}
	if duid == nil {
		mac := make(net.HardwareAddr, 6)
		if _, err := rand.Read(mac); err != nil {
			return nil, err
		}
		mac[0] = (mac[0] | 0x02) &^ 0x01
		duid = &dhcpv6.DUIDLL{HWType: iana.HWTypeEthernet, LinkLayerAddr: mac}
	}
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO local_state (key, value) VALUES (?, ?)`, duidKey, hex.EncodeToString(duid.ToBytes()))
		return err
	})
	return duid, err
}

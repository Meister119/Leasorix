package ha

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/config"
	"github.com/meister119/leasorix/internal/ha/pb"
	"github.com/meister119/leasorix/internal/lease"
	"github.com/meister119/leasorix/internal/metrics"
	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/internal/store"
)

const (
	maxMsgSize     = 64 << 20
	dumpBatch      = 1000
	maxPushBatch   = 2000
	persistEvery   = 10 * time.Second
	configRetry    = 10 * time.Second
	configPushTime = 60 * time.Second
)

// Options are the collaborators of a Node.
type Options struct {
	Config  config.HAConfig
	Log     *slog.Logger
	DB      *store.DB
	Leases  *lease.Manager
	Scopes  *scope.Store
	Metrics *metrics.HA
	Version string
	// Creds overrides the transport credentials (tests).
	Creds credentials.TransportCredentials
}

// policy is the lock-free view of the state used on the DHCP hot path.
type policy struct {
	state    State
	takeover time.Time
}

// Node is this server's side of the HA relationship.
type Node struct {
	pb.UnimplementedPeerServer

	cfg     config.HAConfig
	log     *slog.Logger
	db      *store.DB
	leases  *lease.Manager
	scopes  *scope.Store
	metrics *metrics.HA
	version string
	creds   credentials.TransportCredentials
	split   split
	leader  bool
	now     func() time.Time
	pol     atomic.Pointer[policy]
	kick    chan struct{}
	outbox  outbox

	// blocked simulates a broken peer connection (tests).
	blocked atomic.Bool

	client pb.PeerClient

	mu          sync.Mutex
	m           *machine
	lastErr     string
	lastPersist time.Time
	resyncing   bool
	lastResync  time.Time
	resyncErr   string
	// configuration: current snapshot (leader) or applied version (partner)
	cfgSnap       *configSnapshot
	cfgVersion    uint64
	cfgHash       []byte
	cfgErr        string
	cfgPushing    bool
	cfgPushedHash []byte
	cfgPushedAt   time.Time
	certExpires   time.Time
	runCtx        context.Context
	// cfgGen counts applied configurations; a resync that overlapped with a
	// configuration change does not count.
	cfgGen uint64
}

// NewNode creates the node, restores the persisted failover state and (on
// the config leader) reads the current configuration.
func NewNode(ctx context.Context, o Options) (*Node, error) {
	cfg := o.Config
	n := &Node{
		cfg: cfg, log: o.Log, db: o.DB, leases: o.Leases, scopes: o.Scopes, metrics: o.Metrics,
		version: o.Version, creds: o.Creds, now: time.Now, kick: make(chan struct{}, 1),
		split: split{
			mode:    cfg.Mode,
			primary: cfg.Role == config.HAPrimary,
			lb:      cfg.LoadBalancePercent,
			reserve: cfg.StandbyReservePercent,
		},
		leader: cfg.Role == cfg.ConfigLeader,
		runCtx: context.Background(),
	}
	n.outbox.wake = make(chan struct{}, 1)
	if n.creds == nil {
		c, err := Credentials(cfg)
		if err != nil {
			return nil, err
		}
		n.creds = c
		if cfg.TLS.Mode == config.HATLSMutual {
			if exp, err := CertExpiry(cfg); err == nil {
				n.certExpires = exp
				n.metrics.CertExpiry(exp)
				if time.Until(exp) < 30*24*time.Hour {
					n.log.Warn("HA node certificate expires soon", "expires", exp)
				}
			}
		}
	}
	prev, err := n.loadState(ctx)
	if err != nil {
		return nil, fmt.Errorf("load HA state: %w", err)
	}
	n.m = newMachine(params{
		secondary:       cfg.Role == config.HASecondary,
		commTimeout:     cfg.CommunicationTimeout,
		autoPartnerDown: cfg.AutoPartnerDown,
		mclt:            cfg.MCLT,
	}, randomSession(), prev, n.now())
	if n.leader {
		snap, err := dumpConfig(ctx, n.db)
		if err != nil {
			return nil, fmt.Errorf("read configuration: %w", err)
		}
		n.cfgSnap, n.cfgVersion, n.cfgHash = snap, snap.Version, snap.Hash
	}
	n.metrics.ConfigVersion(n.cfgVersion)
	n.updatePolicy()
	n.log.Info("HA relationship configured", "node", cfg.NodeID, "peer", cfg.Peer.ID, "peer_address", cfg.Peer.Address,
		"mode", cfg.Mode, "role", cfg.Role, "config_leader", n.leader, "state", n.m.state.String())
	return n, nil
}

func randomSession() uint64 {
	var b [8]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			panic(err)
		}
		if v := binary.BigEndian.Uint64(b[:]); v != 0 {
			return v
		}
	}
}

// SetClock overrides the time source (tests).
func (n *Node) SetClock(now func() time.Time) { n.now = now }

// Kick asks the node to re-evaluate soon (e.g. after a configuration change).
func (n *Node) Kick() {
	select {
	case n.kick <- struct{}{}:
	default:
	}
}

// ---- persisted state ---------------------------------------------------------

func (n *Node) loadState(ctx context.Context) (persisted, error) {
	var p persisted
	rows, err := n.db.R.QueryContext(ctx, `SELECT key, value FROM ha_state`)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return p, err
		}
		switch k {
		case "state":
			p.State = State(pb.State_value[v])
		case "partner_down_since":
			if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
				p.PartnerDownSince = time.UnixMilli(ms)
			}
		case "last_alive":
			if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
				p.LastAlive = time.UnixMilli(ms)
			}
		case "config_version":
			n.cfgVersion, _ = strconv.ParseUint(v, 10, 64)
		case "config_hash":
			n.cfgHash, _ = hex.DecodeString(v)
		}
	}
	return p, rows.Err()
}

func (n *Node) saveState(ctx context.Context, kv map[string]string) error {
	return n.db.WriteTx(ctx, func(tx *sql.Tx) error {
		for k, v := range kv {
			if _, err := tx.ExecContext(ctx, `INSERT INTO ha_state (key, value) VALUES (?, ?)
				ON CONFLICT (key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
				return err
			}
		}
		return nil
	})
}

func stateKV(p persisted) map[string]string {
	kv := map[string]string{
		"state":      p.State.String(),
		"last_alive": strconv.FormatInt(p.LastAlive.UnixMilli(), 10),
	}
	if !p.PartnerDownSince.IsZero() {
		kv["partner_down_since"] = strconv.FormatInt(p.PartnerDownSince.UnixMilli(), 10)
	}
	return kv
}

// ---- lease manager coordination (hot path, lock free) ---------------------------

func (n *Node) updatePolicy() {
	n.pol.Store(&policy{state: n.m.state, takeover: n.m.takeoverAt()})
}

// CanAllocate implements lease.Coordinator.
func (n *Node) CanAllocate(ip uint32) bool {
	p := n.pol.Load()
	switch p.state {
	case StateNormal, StateCommunicationInterrupted:
		return n.split.owns(ip)
	case StatePartnerDown:
		return n.split.owns(ip) || !n.now().Before(p.takeover)
	}
	return false
}

// LeaseLimit implements lease.Coordinator: without a working partner (and
// during recovery) leases are limited to MCLT.
func (n *Node) LeaseLimit() time.Duration {
	switch n.pol.Load().state {
	case StateNormal, StatePartnerDown:
		return 0
	}
	return n.cfg.MCLT
}

// Accept decides whether this server answers a DHCP message of the client.
// directed is true for messages explicitly addressed to this server
// (server identifier or unicast renewal). It returns a drop reason
// otherwise.
func (n *Node) Accept(mac string, directed bool) (bool, string) {
	switch n.pol.Load().state {
	case StateRecover, StateRecoverDone:
		return false, "ha_recover"
	case StateNormal:
		if directed || n.split.responsible(mac) {
			return true, ""
		}
		return false, "ha_partner"
	}
	return true, ""
}

// ConfigReadOnly reports whether configuration changes must be made on the
// partner (this server is not the config leader).
func (n *Node) ConfigReadOnly() bool { return !n.leader }

// ---- replication ---------------------------------------------------------------

type pushReq struct {
	changes []lease.Change
	done    chan bool
}

type outbox struct {
	mu      sync.Mutex
	pending []*pushReq
	wake    chan struct{}
}

func (o *outbox) add(r *pushReq) {
	o.mu.Lock()
	o.pending = append(o.pending, r)
	o.mu.Unlock()
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

func (o *outbox) take() []*pushReq {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []*pushReq
	n := 0
	for len(o.pending) > 0 && (n == 0 || n+len(o.pending[0].changes) <= maxPushBatch) {
		r := o.pending[0]
		o.pending = o.pending[1:]
		out = append(out, r)
		n += len(r.changes)
	}
	return out
}

// Replicate implements lease.Coordinator.
func (n *Node) Replicate(ctx context.Context, changes []lease.Change, critical bool) bool {
	n.mu.Lock()
	connected := n.m.connected
	n.mu.Unlock()
	if !connected {
		// The partner pulls everything when the connection comes back.
		return false
	}
	wait := critical && n.cfg.Replication == config.HAReplSync
	r := &pushReq{changes: changes}
	if wait {
		r.done = make(chan bool, 1)
	}
	n.outbox.add(r)
	if !wait {
		return false
	}
	t := time.NewTimer(n.cfg.ReplicationTimeout)
	defer t.Stop()
	select {
	case ok := <-r.done:
		return ok
	case <-t.C:
		n.replicationFailed(errors.New("timeout waiting for the partner"))
	case <-ctx.Done():
	}
	return false
}

func (n *Node) sender(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-n.outbox.wake:
		}
		for {
			batch := n.outbox.take()
			if len(batch) == 0 {
				break
			}
			msg := &pb.LeaseBatch{}
			for _, r := range batch {
				for _, c := range r.changes {
					msg.Leases = append(msg.Leases, toPB(c))
				}
			}
			start := time.Now()
			err := n.push(ctx, msg)
			n.metrics.Push(time.Since(start), err)
			if err == nil {
				n.metrics.Replicated("out", len(msg.Leases))
			} else if ctx.Err() == nil {
				n.replicationFailed(err)
			}
			for _, r := range batch {
				if r.done != nil {
					r.done <- err == nil
				}
			}
		}
	}
}

func (n *Node) push(ctx context.Context, msg *pb.LeaseBatch) error {
	if n.blocked.Load() {
		return status.Error(codes.Unavailable, "blocked")
	}
	cctx, cancel := context.WithTimeout(ctx, n.cfg.ReplicationTimeout)
	defer cancel()
	_, err := n.client.PushLeases(cctx, msg)
	return err
}

func (n *Node) replicationFailed(err error) {
	n.mu.Lock()
	wasConnected := n.m.connected
	if wasConnected {
		n.m.commFailed()
		n.lastErr = "lease update not confirmed: " + err.Error()
	}
	n.mu.Unlock()
	if wasConnected {
		n.log.Warn("lease replication failed; continuing with communication interrupted rules", "err", err)
		n.evaluate()
	}
}

func toPB(c lease.Change) *pb.Lease {
	l := c.Lease
	return &pb.Lease{
		Ip: l.IP, ScopeId: l.ScopeID, Mac: l.MAC, ClientId: l.ClientID, Hostname: l.Hostname, State: string(l.State),
		StartsUnix: l.Start.Unix(), ExpiresUnix: l.Expires.Unix(), UpdatedUnixMs: l.Updated.UnixMilli(),
		Origin: l.Origin, Deleted: c.Deleted,
	}
}

func fromPB(p *pb.Lease) lease.Change {
	return lease.Change{Deleted: p.GetDeleted(), Lease: lease.Lease{
		IP: p.GetIp(), ScopeID: p.GetScopeId(), MAC: p.GetMac(), ClientID: p.GetClientId(), Hostname: p.GetHostname(),
		State: lease.State(p.GetState()), Start: time.Unix(p.GetStartsUnix(), 0).UTC(), Expires: time.Unix(p.GetExpiresUnix(), 0).UTC(),
		Updated: time.UnixMilli(p.GetUpdatedUnixMs()).UTC(), Origin: p.GetOrigin(),
	}}
}

func validLease(p *pb.Lease) bool {
	switch lease.State(p.GetState()) {
	case lease.StateActive, lease.StateExpired, lease.StateReleased, lease.StateDeclined:
		return true
	}
	return p.GetDeleted()
}

// ---- status / compatibility ----------------------------------------------------

func (n *Node) statusLocked() *pb.Status {
	st := &pb.Status{
		NodeId:                n.cfg.NodeID,
		PeerId:                n.cfg.Peer.ID,
		Mode:                  n.cfg.Mode,
		Role:                  n.cfg.Role,
		ConfigLeader:          n.cfg.ConfigLeader,
		LoadBalancePercent:    uint32(n.cfg.LoadBalancePercent),
		StandbyReservePercent: uint32(n.cfg.StandbyReservePercent),
		McltSeconds:           int64(n.cfg.MCLT / time.Second),
		ConfigVersion:         n.cfgVersion,
		ConfigHash:            n.cfgHash,
		TimeUnixMs:            n.now().UnixMilli(),
		Version:               n.version,
	}
	n.m.fillStatus(st)
	return st
}

// compat checks that the partner is the configured peer with matching
// relationship settings. It returns a description of the first mismatch.
func (n *Node) compat(st *pb.Status) string {
	c := n.cfg
	switch {
	case st.GetNodeId() != c.Peer.ID:
		return fmt.Sprintf("partner reports node id %q, expected %q", st.GetNodeId(), c.Peer.ID)
	case st.GetPeerId() != c.NodeID:
		return fmt.Sprintf("partner is configured for peer %q, this node is %q", st.GetPeerId(), c.NodeID)
	case st.GetMode() != c.Mode:
		return fmt.Sprintf("mode differs (partner %q, here %q)", st.GetMode(), c.Mode)
	case st.GetRole() == c.Role:
		return fmt.Sprintf("both servers are configured as %s", c.Role)
	case st.GetConfigLeader() != c.ConfigLeader:
		return fmt.Sprintf("config_leader differs (partner %q, here %q)", st.GetConfigLeader(), c.ConfigLeader)
	case c.Mode == config.HALoadBalance && int(st.GetLoadBalancePercent()) != c.LoadBalancePercent:
		return fmt.Sprintf("load_balance_percent differs (partner %d, here %d)", st.GetLoadBalancePercent(), c.LoadBalancePercent)
	case c.Mode == config.HAHotStandby && int(st.GetStandbyReservePercent()) != c.StandbyReservePercent:
		return fmt.Sprintf("standby_reserve_percent differs (partner %d, here %d)", st.GetStandbyReservePercent(), c.StandbyReservePercent)
	case time.Duration(st.GetMcltSeconds())*time.Second != c.MCLT:
		return fmt.Sprintf("mclt differs (partner %ds, here %s)", st.GetMcltSeconds(), c.MCLT)
	}
	return ""
}

// ---- main loop -------------------------------------------------------------------

// Run serves the peer protocol and maintains the relationship until ctx
// is cancelled.
func (n *Node) Run(ctx context.Context) error {
	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", n.cfg.Listen)
	if err != nil {
		return fmt.Errorf("ha listen %s: %w", n.cfg.Listen, err)
	}
	srv := grpc.NewServer(
		grpc.Creds(n.creds),
		grpc.MaxRecvMsgSize(maxMsgSize),
		grpc.MaxSendMsgSize(maxMsgSize),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 5 * time.Second, PermitWithoutStream: true}),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 10 * time.Second, Timeout: 5 * time.Second}),
	)
	pb.RegisterPeerServer(srv, n)
	conn, err := grpc.NewClient("passthrough:///"+n.cfg.Peer.Address,
		grpc.WithTransportCredentials(n.creds),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxMsgSize), grpc.MaxCallSendMsgSize(maxMsgSize)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 10 * time.Second, Timeout: 5 * time.Second, PermitWithoutStream: true}),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.Config{BaseDelay: 250 * time.Millisecond, Multiplier: 1.6, Jitter: 0.2, MaxDelay: n.cfg.HeartbeatInterval},
			MinConnectTimeout: n.cfg.CommunicationTimeout / 2,
		}),
	)
	if err != nil {
		_ = lis.Close()
		return err
	}
	n.client = pb.NewPeerClient(conn)
	n.mu.Lock()
	n.runCtx = ctx
	n.mu.Unlock()
	n.log.Info("HA peer listener started", "addr", lis.Addr().String())

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		if err := srv.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			return fmt.Errorf("ha server: %w", err)
		}
		return nil
	})
	g.Go(func() error { return n.sender(gctx) })
	g.Go(func() error {
		n.loop(gctx)
		return nil
	})
	g.Go(func() error {
		<-gctx.Done()
		stopped := make(chan struct{})
		go func() { srv.GracefulStop(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(3 * time.Second):
			srv.Stop()
		}
		return conn.Close()
	})
	err = g.Wait()
	n.mu.Lock()
	p := n.m.persisted(n.now())
	n.mu.Unlock()
	if perr := n.saveState(context.Background(), stateKV(p)); perr != nil {
		n.log.Error("saving HA state", "err", perr)
	}
	return err
}

func (n *Node) loop(ctx context.Context) {
	t := time.NewTicker(n.cfg.HeartbeatInterval)
	defer t.Stop()
	for {
		n.heartbeat(ctx)
		n.refreshConfig(ctx)
		n.evaluate()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-n.kick:
		}
	}
}

func (n *Node) heartbeat(ctx context.Context) {
	if n.blocked.Load() {
		return
	}
	n.mu.Lock()
	st := n.statusLocked()
	n.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, n.cfg.HeartbeatInterval)
	defer cancel()
	resp, err := n.client.Heartbeat(cctx, st)
	n.mu.Lock()
	defer n.mu.Unlock()
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() == codes.FailedPrecondition {
			n.m.setMismatch(s.Message())
			n.lastErr = s.Message()
		} else if ctx.Err() == nil {
			n.lastErr = err.Error()
		}
		return
	}
	if msg := n.compat(resp); msg != "" {
		n.m.setMismatch(msg)
		n.lastErr = msg
		return
	}
	n.m.setMismatch("")
	n.lastErr = ""
	n.m.heartbeat(n.now(), resp, true)
}

// refreshConfig re-reads the configuration on the leader when the version
// counter changed.
func (n *Node) refreshConfig(ctx context.Context) {
	if !n.leader {
		return
	}
	v, err := readConfigVersion(ctx, n.db)
	if err != nil {
		if ctx.Err() == nil {
			n.log.Error("reading config version", "err", err)
		}
		return
	}
	n.mu.Lock()
	cur := n.cfgSnap
	n.mu.Unlock()
	if cur != nil && cur.Version == v {
		return
	}
	snap, err := dumpConfig(ctx, n.db)
	if err != nil {
		n.log.Error("reading configuration", "err", err)
		return
	}
	n.mu.Lock()
	n.cfgSnap, n.cfgVersion = snap, snap.Version
	if !bytes.Equal(n.cfgHash, snap.Hash) {
		n.cfgHash = snap.Hash
		n.log.Debug("configuration changed", "version", snap.Version)
	}
	n.mu.Unlock()
	n.metrics.ConfigVersion(snap.Version)
}

// evaluate steps the state machine and starts follow-up work (resync,
// configuration push, persistence).
func (n *Node) evaluate() {
	n.mu.Lock()
	now := n.now()
	from := n.m.state
	path := n.m.step(now)
	n.updatePolicy()
	connected := n.m.connected
	peerHash := n.m.peer.status.GetConfigHash()
	var (
		startResync    bool
		session, epoch uint64
		gen            uint64
		pushConfig     *configSnapshot
	)
	if n.m.needResync() && !n.resyncing && n.configReadyLocked() {
		n.resyncing = true
		startResync = true
		session, epoch, gen = n.m.peer.session, n.m.peer.epoch, n.cfgGen
	}
	if n.leader && connected && n.cfgSnap != nil && !bytes.Equal(peerHash, n.cfgHash) && !n.cfgPushing &&
		(!bytes.Equal(n.cfgPushedHash, n.cfgHash) || now.Sub(n.cfgPushedAt) > configRetry) {
		n.cfgPushing = true
		pushConfig = n.cfgSnap
	}
	persist := len(path) > 0 || now.Sub(n.lastPersist) >= persistEvery
	var kv map[string]string
	if persist {
		n.lastPersist = now
		kv = stateKV(n.m.persisted(now))
	}
	state := n.m.state
	ctx := n.runCtx
	n.mu.Unlock()

	n.metrics.Connected(connected)
	n.metrics.State(state.String())
	for _, to := range path {
		n.log.Info("HA state changed", "from", from.String(), "to", to.String(), "partner_connected", connected)
		e := audit.New(context.Background(), "ha.state", "ha", n.cfg.NodeID, map[string]any{"from": from.String(), "to": to.String()})
		if err := n.db.WriteTx(context.Background(), func(tx *sql.Tx) error { return audit.Insert(context.Background(), tx, e) }); err != nil {
			n.log.Error("audit HA state change", "err", err)
		}
		from = to
	}
	if kv != nil {
		if err := n.saveState(context.Background(), kv); err != nil {
			n.log.Error("saving HA state", "err", err)
		}
	}
	if startResync {
		go n.resync(ctx, session, epoch, gen)
	}
	if pushConfig != nil {
		go n.pushConfig(ctx, pushConfig)
	}
}

// configReadyLocked: the partner (not leader) resyncs leases only with the
// leader's current configuration, so that scope ids and subnets match.
func (n *Node) configReadyLocked() bool {
	if n.leader {
		return true
	}
	h := n.m.peer.status.GetConfigHash()
	return len(h) > 0 && bytes.Equal(h, n.cfgHash)
}

func (n *Node) resync(ctx context.Context, session, epoch, gen uint64) {
	start := time.Now()
	total, applied, conflicts, err := n.pull(ctx)
	n.mu.Lock()
	n.resyncing = false
	if err == nil && gen != n.cfgGen {
		err = errors.New("configuration changed during resync; repeating")
	}
	if err == nil {
		n.m.resyncDone(session, epoch)
		n.lastResync = n.now()
		n.resyncErr = ""
	} else {
		n.resyncErr = err.Error()
	}
	n.mu.Unlock()
	if err != nil {
		if ctx.Err() == nil {
			n.metrics.Resync("error")
			n.log.Warn("lease resync with partner failed", "err", err)
		}
		return
	}
	n.metrics.Resync("ok")
	n.log.Info("lease resync with partner completed", "records", total, "changed", applied, "conflicts", conflicts,
		"duration", time.Since(start).Round(time.Millisecond).String())
	n.evaluate()
	n.Kick()
}

func (n *Node) pull(ctx context.Context) (total, applied, conflicts int, err error) {
	if n.blocked.Load() {
		return 0, 0, 0, errors.New("blocked")
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	stream, err := n.client.DumpLeases(cctx, &pb.DumpRequest{})
	if err != nil {
		return 0, 0, 0, err
	}
	for {
		batch, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return total, applied, conflicts, nil
		}
		if err != nil {
			return total, applied, conflicts, err
		}
		changes := make([]lease.Change, 0, len(batch.GetLeases()))
		for _, l := range batch.GetLeases() {
			if validLease(l) {
				changes = append(changes, fromPB(l))
			}
		}
		a, c, err := n.leases.ApplyRemote(ctx, n.scopes.Snapshot(), changes)
		if err != nil {
			return total, applied, conflicts, err
		}
		total += len(changes)
		applied += a
		conflicts += c
		n.metrics.Replicated("in", len(changes))
		for range c {
			n.metrics.Conflict()
		}
	}
}

func (n *Node) pushConfig(ctx context.Context, snap *configSnapshot) {
	var err error
	if n.blocked.Load() {
		err = errors.New("blocked")
	} else {
		cctx, cancel := context.WithTimeout(ctx, configPushTime)
		_, err = n.client.PushConfig(cctx, &pb.ConfigSnapshot{Version: snap.Version, Hash: snap.Hash, Data: snap.Data})
		cancel()
	}
	n.mu.Lock()
	n.cfgPushing = false
	n.cfgPushedHash, n.cfgPushedAt = snap.Hash, n.now()
	if err != nil {
		n.cfgErr = "configuration push failed: " + err.Error()
	} else {
		n.cfgErr = ""
	}
	n.mu.Unlock()
	if err != nil {
		if ctx.Err() == nil {
			n.log.Warn("configuration push to partner failed", "version", snap.Version, "err", err)
		}
		return
	}
	n.log.Info("configuration pushed to partner", "version", snap.Version, "bytes", len(snap.Data))
}

// ---- peer service (server side) -----------------------------------------------

func (n *Node) checkBlocked() error {
	if n.blocked.Load() {
		return status.Error(codes.Unavailable, "blocked")
	}
	return nil
}

// Heartbeat implements pb.PeerServer.
func (n *Node) Heartbeat(_ context.Context, in *pb.Status) (*pb.Status, error) {
	if err := n.checkBlocked(); err != nil {
		return nil, err
	}
	n.mu.Lock()
	if msg := n.compat(in); msg != "" {
		n.m.setMismatch(msg)
		n.lastErr = msg
		n.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, msg)
	}
	n.m.heartbeat(n.now(), in, false)
	out := n.statusLocked()
	n.mu.Unlock()
	// Evaluate right away, but do not trigger an own heartbeat (that would
	// ping-pong between the servers).
	n.evaluate()
	return out, nil
}

// PushLeases implements pb.PeerServer.
func (n *Node) PushLeases(ctx context.Context, in *pb.LeaseBatch) (*pb.PushReply, error) {
	if err := n.checkBlocked(); err != nil {
		return nil, err
	}
	n.mu.Lock()
	mismatch := n.m.mismatch
	n.mu.Unlock()
	if mismatch != "" {
		return nil, status.Error(codes.FailedPrecondition, mismatch)
	}
	changes := make([]lease.Change, 0, len(in.GetLeases()))
	for _, l := range in.GetLeases() {
		if validLease(l) {
			changes = append(changes, fromPB(l))
		}
	}
	_, conflicts, err := n.leases.ApplyRemote(audit.WithActor(ctx, "ha:"+n.cfg.Peer.ID, ""), n.scopes.Snapshot(), changes)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "storing leases: %v", err)
	}
	n.metrics.Replicated("in", len(changes))
	for range conflicts {
		n.metrics.Conflict()
	}
	return &pb.PushReply{}, nil
}

// DumpLeases implements pb.PeerServer.
func (n *Node) DumpLeases(_ *pb.DumpRequest, stream grpc.ServerStreamingServer[pb.LeaseBatch]) error {
	if err := n.checkBlocked(); err != nil {
		return err
	}
	all := n.leases.All()
	for i := 0; i < len(all); i += dumpBatch {
		end := min(i+dumpBatch, len(all))
		b := &pb.LeaseBatch{Leases: make([]*pb.Lease, 0, end-i)}
		for _, l := range all[i:end] {
			b.Leases = append(b.Leases, toPB(lease.Change{Lease: l}))
		}
		if err := stream.Send(b); err != nil {
			return err
		}
	}
	return nil
}

// PushConfig implements pb.PeerServer: the partner applies the leader's
// configuration.
func (n *Node) PushConfig(ctx context.Context, in *pb.ConfigSnapshot) (*pb.ConfigReply, error) {
	if err := n.checkBlocked(); err != nil {
		return nil, err
	}
	if n.leader {
		return nil, status.Error(codes.FailedPrecondition, "this server is the config leader and does not accept configuration")
	}
	sum := sha256.Sum256(in.GetData())
	if !bytes.Equal(sum[:], in.GetHash()) {
		return nil, status.Error(codes.InvalidArgument, "configuration hash mismatch")
	}
	if err := applyConfig(ctx, n.db, in.GetData()); err != nil {
		n.mu.Lock()
		n.cfgErr = "applying configuration failed: " + err.Error()
		n.mu.Unlock()
		n.log.Error("applying configuration from leader failed", "version", in.GetVersion(), "err", err)
		return nil, status.Errorf(codes.Internal, "applying configuration: %v", err)
	}
	if err := n.saveState(ctx, map[string]string{
		"config_version": strconv.FormatUint(in.GetVersion(), 10),
		"config_hash":    hex.EncodeToString(in.GetHash()),
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "saving configuration version: %v", err)
	}
	if err := n.scopes.Load(ctx); err != nil {
		return nil, status.Errorf(codes.Internal, "reloading configuration: %v", err)
	}
	e := audit.New(audit.WithActor(ctx, "ha:"+n.cfg.Peer.ID, ""), "ha.config_applied", "ha", n.cfg.NodeID,
		map[string]any{"version": in.GetVersion(), "bytes": len(in.GetData())})
	if err := n.db.WriteTx(ctx, func(tx *sql.Tx) error { return audit.Insert(ctx, tx, e) }); err != nil {
		n.log.Error("audit config apply", "err", err)
	}
	n.mu.Lock()
	n.cfgVersion, n.cfgHash, n.cfgErr = in.GetVersion(), in.GetHash(), ""
	n.cfgGen++
	// Scope ids or subnets may have changed: pull the leases again.
	n.m.resyncDone(0, 0)
	n.mu.Unlock()
	n.metrics.ConfigVersion(in.GetVersion())
	n.log.Info("configuration from leader applied", "version", in.GetVersion())
	n.Kick()
	return &pb.ConfigReply{}, nil
}

// ---- operator actions & status ------------------------------------------------

// PartnerDown is the operator confirmation that the partner is down.
func (n *Node) PartnerDown(ctx context.Context) error {
	n.mu.Lock()
	err := n.m.partnerDown(n.now())
	n.updatePolicy()
	takeover := n.m.takeoverAt()
	n.mu.Unlock()
	if err != nil {
		return err
	}
	n.log.Warn("PARTNER_DOWN declared by operator; the whole pool is used after MCLT", "takeover_at", takeover)
	e := audit.New(ctx, "ha.partner_down", "ha", n.cfg.NodeID, map[string]any{"takeover_at": takeover.UTC().Format(time.RFC3339)})
	if err := n.db.WriteTx(ctx, func(tx *sql.Tx) error { return audit.Insert(ctx, tx, e) }); err != nil {
		n.log.Error("audit partner down", "err", err)
	}
	n.mu.Lock()
	kv := stateKV(n.m.persisted(n.now()))
	n.mu.Unlock()
	if err := n.saveState(ctx, kv); err != nil {
		return err
	}
	n.metrics.State(StatePartnerDown.String())
	return nil
}

// Status describes the relationship for the API and UI.
type Status struct {
	NodeID, PeerID, PeerAddress     string
	Mode, Role, Replication         string
	State                           string
	StateSince                      time.Time
	Serving                         bool
	Connected                       bool
	PeerState                       string
	PeerVersion                     string
	LastContact                     time.Time
	PartnerDownSince, TakeoverAt    time.Time
	ConfigLeader, ConfigReadOnly    bool
	ConfigVersion                   uint64
	ConfigInSync                    bool
	LoadBalancePercent              int
	StandbyReservePercent           int
	MCLT                            time.Duration
	AutoPartnerDown                 time.Duration
	LastResync                      time.Time
	Error, ResyncError, ConfigError string
	CertExpires                     time.Time
	TLSMode                         string
}

// Status returns the current status.
func (n *Node) Status() Status {
	n.mu.Lock()
	defer n.mu.Unlock()
	m := n.m
	s := Status{
		NodeID: n.cfg.NodeID, PeerID: n.cfg.Peer.ID, PeerAddress: n.cfg.Peer.Address,
		Mode: n.cfg.Mode, Role: n.cfg.Role, Replication: n.cfg.Replication,
		State: m.state.String(), StateSince: m.since, Serving: m.serving(), Connected: m.connected,
		LastContact: m.lastContact, TakeoverAt: m.takeoverAt(),
		ConfigLeader: n.leader, ConfigReadOnly: !n.leader, ConfigVersion: n.cfgVersion,
		LoadBalancePercent: n.cfg.LoadBalancePercent, StandbyReservePercent: n.cfg.StandbyReservePercent,
		MCLT: n.cfg.MCLT, AutoPartnerDown: n.cfg.AutoPartnerDown,
		LastResync: n.lastResync, Error: n.lastErr, ResyncError: n.resyncErr, ConfigError: n.cfgErr,
		CertExpires: n.certExpires, TLSMode: n.cfg.TLS.Mode,
	}
	if m.state == StatePartnerDown {
		s.PartnerDownSince = m.partnerDownSince
	}
	if m.peer.valid {
		s.PeerState = m.peer.state.String()
		s.PeerVersion = m.peer.status.GetVersion()
		s.ConfigInSync = bytes.Equal(m.peer.status.GetConfigHash(), n.cfgHash)
	}
	if m.mismatch != "" {
		s.Error = m.mismatch
	}
	return s
}

package ha

import (
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/ha/pb"
)

// pair simulates two servers exchanging heartbeats once per second.
type pair struct {
	t      *testing.T
	now    time.Time
	a, b   *machine
	linkUp bool
	aUp    bool
	bUp    bool
	p      params
}

func newPair(t *testing.T, autoPD time.Duration) *pair {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := params{commTimeout: 10 * time.Second, autoPartnerDown: autoPD, mclt: time.Hour}
	ps := p
	ps.secondary = true
	return &pair{
		t: t, now: now, p: p, linkUp: true, aUp: true, bUp: true,
		a: newMachine(p, 1, persisted{}, now),
		b: newMachine(ps, 2, persisted{}, now),
	}
}

func statusOf(m *machine) *pb.Status {
	st := &pb.Status{}
	m.fillStatus(st)
	return st
}

// tick advances one second: heartbeats in both directions (if possible),
// instant resyncs and state steps.
func (p *pair) tick() {
	p.now = p.now.Add(time.Second)
	if p.linkUp && p.aUp && p.bUp {
		p.b.heartbeat(p.now, statusOf(p.a), false)
		p.a.heartbeat(p.now, statusOf(p.b), true)
		p.a.heartbeat(p.now, statusOf(p.b), false)
		p.b.heartbeat(p.now, statusOf(p.a), true)
	}
	for _, m := range []*machine{p.a, p.b} {
		if (m == p.a && !p.aUp) || (m == p.b && !p.bUp) {
			continue
		}
		m.step(p.now)
		if m.needResync() {
			m.resyncDone(m.peer.session, m.peer.epoch)
			m.step(p.now)
		}
	}
}

func (p *pair) run(seconds int) {
	for range seconds {
		p.tick()
	}
}

func (p *pair) want(a, b State) {
	p.t.Helper()
	if p.a.state != a || p.b.state != b {
		p.t.Fatalf("at %s: states = %s/%s, want %s/%s", p.now.Format(time.TimeOnly), p.a.state, p.b.state, a, b)
	}
}

func TestMachineStartupReachesNormal(t *testing.T) {
	p := newPair(t, 0)
	p.want(StateRecover, StateRecover)
	if p.a.serving() || p.b.serving() {
		t.Fatal("servers must not serve before the first resync")
	}
	p.run(3)
	p.want(StateNormal, StateNormal)
}

func TestMachinePartitionAndHeal(t *testing.T) {
	p := newPair(t, 0)
	p.run(3)
	p.linkUp = false
	p.run(9)
	p.want(StateNormal, StateNormal)
	p.run(2)
	p.want(StateCommunicationInterrupted, StateCommunicationInterrupted)
	// Without automatic partner-down they stay there forever.
	p.run(7200)
	p.want(StateCommunicationInterrupted, StateCommunicationInterrupted)
	p.linkUp = true
	p.run(1)
	if p.a.serving() && p.a.state == StateNormal {
		t.Fatal("must resync before normal operation")
	}
	p.run(3)
	p.want(StateNormal, StateNormal)
}

func TestMachinePartnerCrashAutoPartnerDownAndRecovery(t *testing.T) {
	p := newPair(t, 30*time.Second)
	p.run(3)
	p.bUp = false
	p.run(11)
	p.want(StateCommunicationInterrupted, StateNormal)
	p.run(30)
	if p.a.state != StatePartnerDown {
		t.Fatalf("a = %s, want PARTNER_DOWN", p.a.state)
	}
	if !p.a.takeoverAt().Equal(p.a.partnerDownSince.Add(time.Hour)) {
		t.Fatal("takeover must wait MCLT")
	}
	// b restarts after a long downtime: no automatic resume.
	p.b = newMachine(p.b.p, 3, persisted{State: StateNormal, LastAlive: p.now.Add(-41 * time.Second)}, p.now)
	p.bUp = true
	if !p.b.resumeAt.IsZero() {
		t.Fatal("a server down longer than auto_partner_down must not resume on its own")
	}
	p.run(1)
	if p.a.state != StatePartnerDown || p.b.state == StateNormal {
		t.Fatalf("states %s/%s: partner-down server keeps serving until the partner is recovered", p.a.state, p.b.state)
	}
	p.run(4)
	p.want(StateNormal, StateNormal)
}

func TestMachineRestartResumesWhenPartnerUnreachable(t *testing.T) {
	p := newPair(t, 0)
	p.run(3)
	p.linkUp = false
	p.b = newMachine(p.b.p, 9, persisted{State: StateNormal, LastAlive: p.now.Add(-5 * time.Second)}, p.now)
	p.run(9)
	if p.b.state != StateRecover || p.b.serving() {
		t.Fatal("restarted server waits for the partner first")
	}
	p.run(2)
	if p.b.state != StateCommunicationInterrupted {
		t.Fatalf("b = %s, want COMMUNICATION_INTERRUPTED after the startup wait", p.b.state)
	}
}

func TestMachineNeverSyncedServerWaits(t *testing.T) {
	p := newPair(t, time.Minute)
	p.linkUp = false
	p.run(3600)
	p.want(StateRecover, StateRecover)
	if err := p.a.partnerDown(p.now); err != nil {
		t.Fatal(err)
	}
	p.want(StatePartnerDown, StateRecover)
	if err := p.a.partnerDown(p.now); err == nil {
		t.Fatal("double partner-down must fail")
	}
}

func TestMachineManualPartnerDownRefusedWhileConnected(t *testing.T) {
	p := newPair(t, 0)
	p.run(3)
	if err := p.a.partnerDown(p.now); err == nil {
		t.Fatal("partner-down must be refused while the partner is reachable")
	}
}

func TestMachineBothPartnerDownSecondaryYields(t *testing.T) {
	p := newPair(t, 20*time.Second)
	p.run(3)
	p.linkUp = false
	p.run(40)
	p.want(StatePartnerDown, StatePartnerDown)
	p.linkUp = true
	p.run(1)
	if p.b.state == StatePartnerDown {
		t.Fatal("secondary must leave PARTNER_DOWN when both took over")
	}
	p.run(4)
	p.want(StateNormal, StateNormal)
}

func TestMachineReplicationFailureForcesResync(t *testing.T) {
	p := newPair(t, 0)
	p.run(3)
	epoch := p.a.epoch
	p.a.commFailed()
	p.a.step(p.now)
	if p.a.state != StateCommunicationInterrupted || p.a.epoch != epoch+1 {
		t.Fatalf("state %s epoch %d", p.a.state, p.a.epoch)
	}
	p.run(4)
	p.want(StateNormal, StateNormal)
	if p.b.syncedEpoch != p.a.epoch {
		t.Fatal("partner must have resynced against the new epoch")
	}
}

// A change that could not be sent while disconnected must reach the partner
// even if it already resynced earlier in the same interruption.
func TestMachineUnsentChangeForcesAnotherResync(t *testing.T) {
	p := newPair(t, 0)
	p.run(3)
	p.a.commFailed()
	p.a.step(p.now)
	// The partner pulls a's table before a's next change ...
	p.b.heartbeat(p.now, statusOf(p.a), false)
	p.b.step(p.now)
	if !p.b.needResync() {
		t.Fatal("partner must resync after the interruption")
	}
	p.b.resyncDone(p.a.session, p.a.epoch)
	// ... which a then cannot send.
	p.a.changeNotSent()
	p.b.heartbeat(p.now, statusOf(p.a), false)
	if !p.b.needResync() {
		t.Fatal("partner must pull again after an unsent change")
	}
	p.run(5)
	p.want(StateNormal, StateNormal)
	if p.b.syncedEpoch != p.a.epoch {
		t.Fatal("partner must have resynced against the latest epoch")
	}
}

func TestMachineMismatchBlocksConnection(t *testing.T) {
	p := newPair(t, 0)
	// Both sides detect it (the receiver rejects the heartbeat).
	p.a.setMismatch("mode differs")
	p.b.setMismatch("mode differs")
	p.run(5)
	p.want(StateRecover, StateRecover)
	p.a.setMismatch("")
	p.b.setMismatch("")
	p.run(3)
	p.want(StateNormal, StateNormal)
}

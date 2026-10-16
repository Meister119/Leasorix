package ha

import (
	"errors"
	"time"

	"github.com/meister119/leasorix/internal/ha/pb"
)

// State is the failover state of this server in the relationship.
type State = pb.State

// The states of the relationship (see docs/HA.md for the full picture).
const (
	StateNormal                   = pb.State_NORMAL
	StateCommunicationInterrupted = pb.State_COMMUNICATION_INTERRUPTED
	StatePartnerDown              = pb.State_PARTNER_DOWN
	StateRecover                  = pb.State_RECOVER
	StateRecoverDone              = pb.State_RECOVER_DONE
)

// Errors of operator actions.
var (
	ErrPartnerReachable = errors.New("the partner is reachable; PARTNER_DOWN can only be declared while communication is interrupted")
	ErrAlreadyDown      = errors.New("PARTNER_DOWN is already active")
)

// params are the timing settings of the state machine.
type params struct {
	secondary       bool
	commTimeout     time.Duration
	autoPartnerDown time.Duration
	mclt            time.Duration
}

// peerView is the last status received from the partner.
type peerView struct {
	valid         bool
	session       uint64
	epoch         uint64
	state         State
	syncedSession uint64
	syncedEpoch   uint64
	status        *pb.Status
}

// persisted is the part of the machine that survives restarts.
type persisted struct {
	State            State
	PartnerDownSince time.Time
	// LastAlive is the last time this server was running (updated
	// periodically), used to estimate the downtime after a restart.
	LastAlive time.Time
}

// machine is the failover state machine. It is purely event driven and
// takes the current time as argument, so it can be tested with simulated
// time. It is not safe for concurrent use; Node serialises access.
type machine struct {
	p       params
	session uint64

	state            State
	since            time.Time
	partnerDownSince time.Time

	// epoch increments whenever communication is lost, so the partner can
	// tell that changes may have been missed and a resync is due.
	epoch       uint64
	connected   bool
	lastOut     time.Time // last successful outgoing heartbeat
	lastIn      time.Time // last heartbeat received from the partner
	lastContact time.Time
	mismatch    string
	peer        peerView

	// The partner session/epoch this server last completed a resync against.
	syncedSession uint64
	syncedEpoch   uint64

	// resumeAt: after a restart with an up-to-date lease table, switch from
	// RECOVER to COMMUNICATION_INTERRUPTED at this time if the partner is
	// still unreachable (zero = wait for the partner or an operator).
	resumeAt time.Time
}

func newMachine(p params, session uint64, prev persisted, now time.Time) *machine {
	m := &machine{p: p, session: session, epoch: 1, state: StateRecover, since: now}
	switch prev.State {
	case StatePartnerDown:
		m.state = StatePartnerDown
		m.partnerDownSince = prev.PartnerDownSince
	case StateNormal, StateCommunicationInterrupted, StateRecoverDone:
		// The lease table was complete before the restart. Serving our own
		// share is safe unless the partner may have taken over the whole
		// pool automatically in the meantime.
		down := now.Sub(prev.LastAlive)
		if prev.LastAlive.IsZero() || p.autoPartnerDown == 0 || down < p.autoPartnerDown {
			m.resumeAt = now.Add(p.commTimeout)
		}
	}
	return m
}

func (m *machine) persisted(now time.Time) persisted {
	return persisted{State: m.state, PartnerDownSince: m.partnerDownSince, LastAlive: now}
}

// heartbeat records a successful exchange with the partner. outgoing is
// true for heartbeats we sent (and got an answer to).
func (m *machine) heartbeat(now time.Time, st *pb.Status, outgoing bool) {
	if outgoing {
		m.lastOut = now
	} else {
		m.lastIn = now
	}
	m.peer = peerView{
		valid:         true,
		session:       st.GetSession(),
		epoch:         st.GetEpoch(),
		state:         st.GetState(),
		syncedSession: st.GetSyncedSession(),
		syncedEpoch:   st.GetSyncedEpoch(),
		status:        st,
	}
}

// setMismatch marks the partner as incompatible (msg != "") or clears it.
func (m *machine) setMismatch(msg string) { m.mismatch = msg }

// commFailed forces the loss of communication, e.g. when a synchronous
// lease update could not be delivered.
func (m *machine) commFailed() { m.lastOut = time.Time{} }

// changeNotSent records a lease change that could not be sent to the
// partner. The new epoch makes the partner pull our table (again) after the
// change, even if it already resynced earlier in this interruption.
func (m *machine) changeNotSent() { m.epoch++ }

func (m *machine) commOK(now time.Time) bool {
	if !m.peer.valid || m.mismatch != "" || m.lastOut.IsZero() || m.lastIn.IsZero() {
		return false
	}
	return now.Sub(m.lastOut) < m.p.commTimeout && now.Sub(m.lastIn) < m.p.commTimeout
}

// resyncDone records that the lease table of the partner (in the given
// session/epoch) has been pulled and merged.
func (m *machine) resyncDone(session, epoch uint64) {
	m.syncedSession, m.syncedEpoch = session, epoch
}

func (m *machine) ourSyncDone() bool {
	return m.connected && m.peer.session != 0 && m.syncedSession == m.peer.session && m.syncedEpoch == m.peer.epoch
}

func (m *machine) peerSynced() bool {
	return m.peer.syncedSession == m.session && m.peer.syncedEpoch == m.epoch
}

// needResync reports whether the partner's lease table must be pulled.
func (m *machine) needResync() bool {
	return m.connected && !m.ourSyncDone() && m.state != StateCommunicationInterrupted
}

// partnerDown is the operator confirmation that the partner is down.
func (m *machine) partnerDown(now time.Time) error {
	switch {
	case m.state == StatePartnerDown:
		return ErrAlreadyDown
	case m.connected || m.state == StateNormal || m.state == StateRecoverDone:
		return ErrPartnerReachable
	}
	m.enter(StatePartnerDown, now)
	return nil
}

func (m *machine) enter(s State, now time.Time) {
	m.state, m.since = s, now
	if s == StatePartnerDown {
		m.partnerDownSince = now
	}
	if s != StateRecover {
		m.resumeAt = time.Time{}
	}
}

// step evaluates the transitions at time now and returns the states it
// passed through (empty if nothing changed).
func (m *machine) step(now time.Time) []State {
	ok := m.commOK(now)
	if ok {
		m.lastContact = now
	}
	if m.connected && !ok {
		m.connected = false
		m.epoch++
	} else if !m.connected && ok {
		m.connected = true
	}
	var path []State
	for range 4 {
		next, changed := m.next(now, ok)
		if !changed {
			break
		}
		m.enter(next, now)
		path = append(path, next)
	}
	return path
}

func (m *machine) next(now time.Time, ok bool) (State, bool) {
	peerState := m.peer.state
	switch m.state {
	case StateNormal:
		if !ok {
			return StateCommunicationInterrupted, true
		}
		if peerState == StatePartnerDown {
			// The partner serves the whole pool: stop and resync.
			return StateRecover, true
		}
	case StateCommunicationInterrupted:
		if ok {
			return StateRecover, true
		}
		if m.p.autoPartnerDown > 0 && now.Sub(m.since) >= m.p.autoPartnerDown {
			return StatePartnerDown, true
		}
	case StateRecover:
		if ok && m.ourSyncDone() {
			return StateRecoverDone, true
		}
		if !ok && !m.resumeAt.IsZero() && !now.Before(m.resumeAt) {
			return StateCommunicationInterrupted, true
		}
	case StateRecoverDone:
		if !ok || !m.ourSyncDone() {
			return StateRecover, true
		}
		if m.peerSynced() && (peerState == StateRecoverDone || peerState == StateNormal) {
			return StateNormal, true
		}
	case StatePartnerDown:
		if !ok {
			break
		}
		if peerState == StatePartnerDown && m.p.secondary {
			// Both servers took over the whole pool (partition with
			// automatic partner-down): the secondary yields.
			return StateRecover, true
		}
		if m.ourSyncDone() && m.peerSynced() && peerState == StateRecoverDone {
			return StateNormal, true
		}
	}
	return m.state, false
}

// serving reports whether this server answers DHCP clients at all.
func (m *machine) serving() bool {
	return m.state != StateRecover && m.state != StateRecoverDone
}

// takeoverAt is the time from which a server in PARTNER_DOWN may allocate
// addresses of the partner's share (zero if not in PARTNER_DOWN).
func (m *machine) takeoverAt() time.Time {
	if m.state != StatePartnerDown {
		return time.Time{}
	}
	return m.partnerDownSince.Add(m.p.mclt)
}

// fillStatus sets the state machine fields of a status message.
func (m *machine) fillStatus(st *pb.Status) {
	st.State = m.state
	st.Session = m.session
	st.Epoch = m.epoch
	st.SyncedSession = m.syncedSession
	st.SyncedEpoch = m.syncedEpoch
}

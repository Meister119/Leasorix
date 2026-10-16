#!/usr/bin/env bash
# End-to-end test: runs leasorixd (with embedded UI) in a network namespace,
# a DHCP client namespace connected to it, and drives the web UI with
# Playwright. Requires root, iproute2, udhcpc and a built bin/leasorixd.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="${BIN:-$ROOT/bin/leasorixd}"
SFX="$(head -c3 /dev/urandom | od -An -tx1 | tr -d ' \n')"
SRV="lxe2es$SFX"
CLI="lxe2ec$SFX"
MGMT="lxm$SFX"
WORK="$(mktemp -d)"
TOKEN="e2e-setup-$SFX"

cleanup() {
  [ -n "${PID:-}" ] && kill "$PID" 2>/dev/null && wait "$PID" 2>/dev/null || true
  ip netns del "$SRV" 2>/dev/null || true
  ip netns del "$CLI" 2>/dev/null || true
  ip link del "$MGMT" 2>/dev/null || true
  if [ "${KEEP:-0}" != 1 ]; then rm -rf "$WORK"; else echo "kept $WORK"; fi
}
trap cleanup EXIT

[ -x "$BIN" ] || { echo "missing $BIN (run make build)"; exit 1; }

ip netns add "$SRV"
ip netns add "$CLI"
# DHCP link: srv/s0 10.77.0.1/16 <-> cli/c0
ip link add "s$SFX" type veth peer name "c$SFX"
ip link set "s$SFX" netns "$SRV"
ip link set "c$SFX" netns "$CLI"
ip netns exec "$SRV" sh -ec "ip link set lo up; ip link set s$SFX name s0; ip link set s0 up; ip addr add 10.77.0.1/16 dev s0"
ip netns exec "$CLI" sh -ec "ip link set lo up; ip link set c$SFX name c0; ip link set c0 up"
# Management link: root/$MGMT 10.250.0.1/30 <-> srv/m0 10.250.0.2/30
ip link add "$MGMT" type veth peer name "mp$SFX"
ip link set "mp$SFX" netns "$SRV"
ip addr add 10.250.0.1/30 dev "$MGMT"
ip link set "$MGMT" up
ip netns exec "$SRV" sh -ec "ip link set mp$SFX name m0; ip link set m0 up; ip addr add 10.250.0.2/30 dev m0"

cat >"$WORK/leasorix.yml" <<EOF
data_dir: $WORK/data
log: {level: debug, format: text}
http:
  listen: 10.250.0.2:8443
  tls: {mode: self-signed}
auth:
  setup_token: $TOKEN
dhcp4:
  interfaces: [s0]
EOF

ip netns exec "$SRV" "$BIN" --config "$WORK/leasorix.yml" >"$WORK/server.log" 2>&1 &
PID=$!
for _ in $(seq 1 50); do
  curl -sk --max-time 1 https://10.250.0.2:8443/readyz >/dev/null 2>&1 && break
  sleep 0.2
done

cd "$ROOT/web"
status=0
E2E_BASE_URL="https://10.250.0.2:8443" E2E_SETUP_TOKEN="$TOKEN" E2E_CLIENT_NS="$CLI" npx playwright test "$@" || status=$?
if [ "$status" != 0 ]; then
  echo "---- server log (tail) ----"
  tail -n 80 "$WORK/server.log"
fi
exit "$status"

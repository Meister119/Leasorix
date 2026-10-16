#!/usr/bin/env bash
# Tests the container image and the compose files (make test-docker):
#   1. docker-compose.yml        host networking: UI reachable, client gets a lease
#   2. docker-compose.macvlan.yml own LAN address: client gets a lease
#   3. docker-compose.ha.yml      two containers: config sync, replicated lease,
#                                 the partner keeps serving after the primary is killed
# Each test uses an isolated L2 segment: a veth pair whose host end is the
# DHCP interface (host mode) or the macvlan parent, the other end lives in a
# client network namespace. Requires root, docker with compose, iproute2,
# udhcpc and curl. IMAGE selects the image (default leasorix:latest).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
IMAGE="${IMAGE:-leasorix:latest}"
SFX="$(head -c2 /dev/urandom | od -An -tx1 | tr -d ' \n')"
TOKEN="docker-test-setup-$SFX"
WORK="$(mktemp -d)"
PROJECTS=()
NETNS=()
LINKS=()

cleanup() {
  set +e
  for p in "${PROJECTS[@]}"; do
    if [ "${FAILED:-0}" = 1 ]; then docker compose -p "$p" logs --tail 60 2>/dev/null; fi
    docker compose -p "$p" down -v --remove-orphans >/dev/null 2>&1
  done
  for n in "${NETNS[@]}"; do ip netns del "$n" 2>/dev/null; done
  for l in "${LINKS[@]}"; do ip link del "$l" 2>/dev/null; done
  rm -rf "$WORK"
}
trap 'FAILED=1' ERR
trap cleanup EXIT

log() { printf '\n== %s\n' "$*"; }
fail() { echo "FAIL: $*" >&2; FAILED=1; exit 1; }

# segment NAME: veth "lxd$NAME" in the host, "c0" in netns "lxdc$NAME".
segment() {
  local name=$1
  ip netns add "lxdc$name"; NETNS+=("lxdc$name")
  ip link add "lxd$name" type veth peer name "lxp$name"; LINKS+=("lxd$name")
  ip link set "lxp$name" netns "lxdc$name"
  ip netns exec "lxdc$name" sh -ec "ip link set lo up; ip link set lxp$name name c0; ip link set c0 up"
  ip link set "lxd$name" up
}

# udhcpc_lease runs udhcpc once and prints "ip serverid".
cat >"$WORK/udhcpc.sh" <<'S'
#!/bin/sh
[ "$1" = bound ] && echo "LEASE $ip $serverid" >>"$LEASE_FILE"
exit 0
S
chmod +x "$WORK/udhcpc.sh"
udhcpc_lease() { # NETNS MAC [REQUESTED_IP]
  local ns=$1 mac=$2 req=${3:-} out="$WORK/lease.$RANDOM"
  local args=(-i c0 -f -q -n -t 8 -T 2 -s "$WORK/udhcpc.sh")
  [ -n "$req" ] && args+=(-r "$req")
  ip netns exec "$ns" ip link set c0 address "$mac"
  LEASE_FILE="$out" ip netns exec "$ns" udhcpc "${args[@]}" >/dev/null 2>&1 || true
  [ -s "$out" ] && awk '/^LEASE/ {print $2, $3}' "$out" | tail -1
}

# api NETNS BASE_URL JAR METHOD PATH [JSON]
api() {
  local ns=$1 base=$2 jar=$3 method=$4 path=$5 body=${6:-}
  local csrf=""
  [ -f "$jar.csrf" ] && csrf=$(cat "$jar.csrf")
  local args=(-sk --max-time 10 -b "$jar" -c "$jar" -X "$method" -H "X-CSRF-Token: $csrf")
  [ -n "$body" ] && args+=(-H "Content-Type: application/json" --data "$body")
  if [ -n "$ns" ]; then ip netns exec "$ns" curl "${args[@]}" "$base/api/v1$path"; else curl "${args[@]}" "$base/api/v1$path"; fi
}

session() { # NETNS BASE JAR PATH BODY -> stores csrf
  api "$1" "$2" "$3" POST "$4" "$5" | sed -n 's/.*"csrf_token":"\([^"]*\)".*/\1/p' >"$3.csrf"
  [ -s "$3.csrf" ] || fail "no session from $2$4"
}

wait_for() { # SECONDS DESCRIPTION COMMAND...
  local t=$1 what=$2; shift 2
  for _ in $(seq 1 $((t * 2))); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 0.5
  done
  fail "timeout waiting for $what"
}

healthy() { [ "$(docker inspect -f '{{.State.Health.Status}}' "$1" 2>/dev/null)" = healthy ]; }

ha_state() { # NETNS IP -> state
  ip netns exec "$1" curl -sk --max-time 3 "https://$2:8067/metrics" | awk -F'"' '/^leasorix_ha_state\{/ && $NF ~ / 1$/ {print $2}'
}

compose() { # PROJECT FILE args...
  local p=$1 f=$2; shift 2
  LEASORIX_IMAGE="$IMAGE" LEASORIX_SETUP_TOKEN="$TOKEN" docker compose -p "$p" -f "$ROOT/deploy/$f" "$@"
}

# ---------------------------------------------------------------------------
log "1. host networking (docker-compose.yml)"
segment h
ip addr add 10.88.0.1/24 dev lxdh
P="lxtest-host-$SFX"; PROJECTS+=("$P")
LEASORIX_DHCP4_INTERFACES=lxdh LEASORIX_HTTP_LISTEN=10.88.0.1:8067 compose "$P" docker-compose.yml up -d --no-build --quiet-pull
CID=$(compose "$P" docker-compose.yml ps -q leasorix)
wait_for 60 "healthy container" healthy "$CID"
[ "$(docker inspect -f '{{.Config.User}}' "$CID")" = "65532:65532" ] || fail "container does not run as nonroot"
[ "$(docker inspect -f '{{.HostConfig.ReadonlyRootfs}} {{.HostConfig.RestartPolicy.Name}}' "$CID")" = "true unless-stopped" ] || fail "hardening/restart policy missing"
PID=$(docker inspect -f '{{.State.Pid}}' "$CID")
grep -q '^CapEff:.*0000000000002400$' "/proc/$PID/status" || fail "unexpected capabilities: $(grep CapEff "/proc/$PID/status")"
curl -sk --max-time 5 https://10.88.0.1:8067/ | grep -q "<title>" || fail "web UI not reachable"
J="$WORK/jar-host"
session "" https://10.88.0.1:8067 "$J" /setup "{\"setup_token\":\"$TOKEN\",\"username\":\"admin\",\"password\":\"admin-password\"}"
api "" https://10.88.0.1:8067 "$J" POST /scopes '{"name":"lan","subnet":"10.88.0.0/24","start":"10.88.0.100","end":"10.88.0.200","lease_time":3600,"enabled":true}' | grep -q '"id"' || fail "create scope"
LEASE=$(udhcpc_lease lxdch 02:00:00:88:00:01)
echo "lease: $LEASE"
[ "${LEASE%% *}" = 10.88.0.100 ] || fail "host mode: client got '$LEASE'"
docker exec "$CID" leasorixd backup --out /data/backup.db || fail "backup inside the container"
API_TOKEN=$(api "" https://10.88.0.1:8067 "$J" POST /tokens '{"name":"ctl","role":"readonly"}' | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
[ -n "$API_TOKEN" ] || fail "create API token"
docker exec -e LEASORIX_TOKEN="$API_TOKEN" "$CID" leasorixctl --url https://10.88.0.1:8067 --insecure scopes list | grep -q 10.88.0.0/24 || fail "leasorixctl in the image"
compose "$P" docker-compose.yml down -v >/dev/null 2>&1

# ---------------------------------------------------------------------------
log "2. macvlan (docker-compose.macvlan.yml)"
segment m
ip netns exec lxdcm ip addr add 10.89.0.250/24 dev c0
P="lxtest-macvlan-$SFX"; PROJECTS+=("$P")
LEASORIX_PARENT=lxdm LEASORIX_SUBNET=10.89.0.0/24 LEASORIX_GATEWAY=10.89.0.1 LEASORIX_IP=10.89.0.10 \
  compose "$P" docker-compose.macvlan.yml up -d --no-build --quiet-pull
CID=$(compose "$P" docker-compose.macvlan.yml ps -q leasorix)
wait_for 60 "healthy container" healthy "$CID"
J="$WORK/jar-macvlan"
session lxdcm https://10.89.0.10:8067 "$J" /setup "{\"setup_token\":\"$TOKEN\",\"username\":\"admin\",\"password\":\"admin-password\"}"
api lxdcm https://10.89.0.10:8067 "$J" POST /scopes '{"name":"lan","subnet":"10.89.0.0/24","start":"10.89.0.100","end":"10.89.0.200","lease_time":3600,"enabled":true}' | grep -q '"id"' || fail "create scope"
LEASE=$(udhcpc_lease lxdcm 02:00:00:89:00:01)
echo "lease: $LEASE"
[ "$LEASE" = "10.89.0.100 10.89.0.10" ] || fail "macvlan: client got '$LEASE'"
LEASORIX_PARENT=lxdm LEASORIX_SUBNET=10.89.0.0/24 compose "$P" docker-compose.macvlan.yml down -v >/dev/null 2>&1

# ---------------------------------------------------------------------------
log "3. high availability (docker-compose.ha.yml)"
segment a
ip netns exec lxdca ip addr add 10.90.0.250/24 dev c0
P="lxtest-ha-$SFX"; PROJECTS+=("$P")
export LEASORIX_PARENT=lxda LEASORIX_SUBNET=10.90.0.0/24 LEASORIX_GATEWAY=10.90.0.1 LEASORIX_A_IP=10.90.0.11 LEASORIX_B_IP=10.90.0.12
compose "$P" docker-compose.ha.yml up -d --no-build --quiet-pull
A=$(compose "$P" docker-compose.ha.yml ps -q dhcp-a)
B=$(compose "$P" docker-compose.ha.yml ps -q dhcp-b)
wait_for 60 "healthy containers" sh -c "[ \"\$(docker inspect -f '{{.State.Health.Status}}' $A)\" = healthy ] && [ \"\$(docker inspect -f '{{.State.Health.Status}}' $B)\" = healthy ]"
wait_for 60 "HA state NORMAL on both" sh -c "[ \"\$(ip netns exec lxdca curl -sk https://10.90.0.11:8067/metrics | grep -c 'leasorix_ha_state{state=\"NORMAL\"} 1')\" = 1 ] && [ \"\$(ip netns exec lxdca curl -sk https://10.90.0.12:8067/metrics | grep -c 'leasorix_ha_state{state=\"NORMAL\"} 1')\" = 1 ]"
JA="$WORK/jar-a"; JB="$WORK/jar-b"
session lxdca https://10.90.0.11:8067 "$JA" /setup "{\"setup_token\":\"$TOKEN\",\"username\":\"admin\",\"password\":\"admin-password\"}"
api lxdca https://10.90.0.11:8067 "$JA" POST /scopes '{"name":"lan","subnet":"10.90.0.0/24","start":"10.90.0.100","end":"10.90.0.200","lease_time":3600,"enabled":true}' | grep -q '"id"' || fail "create scope on the leader"
login_b() { session lxdca https://10.90.0.12:8067 "$JB" /auth/login '{"username":"admin","password":"admin-password"}'; }
wait_for 20 "admin replicated to the partner" login_b
wait_for 20 "scope replicated to the partner" sh -c "ip netns exec lxdca curl -sk -b $JB https://10.90.0.12:8067/api/v1/scopes | grep -q 10.90.0.200"
api lxdca https://10.90.0.12:8067 "$JB" POST /scopes '{"name":"x","subnet":"10.91.0.0/24","start":"10.91.0.10","end":"10.91.0.20","lease_time":3600,"enabled":true}' | grep -q "read-only" || fail "partner accepted a configuration change"
wait_for 20 "both servers serving again" sh -c "[ \"\$(ip netns exec lxdca curl -sk https://10.90.0.12:8067/metrics | grep -c 'leasorix_ha_state{state=\"NORMAL\"} 1')\" = 1 ]"
# Load balancing splits clients by a hash of the MAC: 02:00:00:90:00:02 is
# served by dhcp-a, 02:00:00:90:00:01 by dhcp-b.
MAC_A=02:00:00:90:00:02 MAC_B=02:00:00:90:00:01
LEASE_B=$(udhcpc_lease lxdca $MAC_B)
echo "client B: $LEASE_B"
[ "${LEASE_B#* }" = 10.90.0.12 ] || fail "client B not served by dhcp-b: '$LEASE_B'"
LEASE_A=$(udhcpc_lease lxdca $MAC_A)
echo "client A: $LEASE_A"
IP=${LEASE_A%% *}
[ "${LEASE_A#* }" = 10.90.0.11 ] || fail "client A not served by dhcp-a: '$LEASE_A'"
api lxdca https://10.90.0.12:8067 "$JB" GET "/leases/$IP" | grep -q "$MAC_A" || fail "lease not replicated to the partner"
log "killing dhcp-a"
docker kill "$A" >/dev/null
wait_for 30 "partner in COMMUNICATION_INTERRUPTED" sh -c "ip netns exec lxdca curl -sk https://10.90.0.12:8067/metrics | grep -q 'leasorix_ha_state{state=\"COMMUNICATION_INTERRUPTED\"} 1'"
LEASE2=$(udhcpc_lease lxdca $MAC_A "$IP")
echo "client A after failover: $LEASE2"
[ "$LEASE2" = "$IP 10.90.0.12" ] || fail "client A did not keep its address at the partner: '$LEASE2'"
compose "$P" docker-compose.ha.yml down -v >/dev/null 2>&1

log "all docker tests passed"

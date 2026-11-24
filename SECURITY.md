# Security policy

Leasorix hands out network addresses and holds credentials (users, API
tokens, TSIG keys, HA certificates), so security reports are very welcome.

## Supported versions

Leasorix is in **beta**. Fixes are made on `main` and shipped with the next
release; only the latest release is supported.

## Reporting a vulnerability

**Please do not open a public issue for security problems.**

Report them privately via GitHub:
[Report a vulnerability](https://github.com/meister119/leasorix/security/advisories/new)
(tab *Security* → *Report a vulnerability*).

Please include:

- the affected version (`leasorixd version` or *Settings → System*),
- how Leasorix is deployed (binary, Docker host networking, macvlan, HA),
- steps to reproduce and the impact you see.

You will get a first answer within a few days. Once a fix is released, the
advisory is published and you are credited, unless you prefer not to be.

## Scope

In scope: the `leasorixd` server (DHCPv4/v6, REST API, web UI, HA peer
protocol, DNS updates), `leasorixctl` and the published container image.

Out of scope: attacks that need full access to the host or to the data
directory, and the inherent weaknesses of DHCP itself (for example rogue
DHCP servers or spoofed client requests on an untrusted segment).

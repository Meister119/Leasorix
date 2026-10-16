#!/bin/sh
# Creates the test zones and the TSIG key, then starts PowerDNS.
set -e
pdnsutil create-zone pdns.test ns1.pdns.test
pdnsutil add-record pdns.test ns1 A 10.78.0.54
# created manually: name protection must not overwrite it
pdnsutil add-record pdns.test static A 10.78.9.9
pdnsutil create-zone 76.10.in-addr.arpa ns1.pdns.test
# Test-only key, also in test/dns/bind/named.conf and the Go test.
pdnsutil import-tsig-key leasorix-key hmac-sha256 bGVhc29yaXgtaW50ZWdyYXRpb24tdGVzdC1rZXktMDE=
for z in pdns.test 76.10.in-addr.arpa; do
    pdnsutil set-meta "$z" TSIG-ALLOW-DNSUPDATE leasorix-key
done
exec /usr/bin/tini -- /usr/local/sbin/pdns_server-startup

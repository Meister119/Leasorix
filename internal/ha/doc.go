// Package ha implements the high availability relationship between two
// Leasorix servers: failover state machine (machine.go), split of clients
// and addresses (split.go), lease and configuration replication over an
// authenticated gRPC connection (node.go, configsync.go, transport.go) and
// certificate generation (certs.go). See docs/HA.md.
package ha

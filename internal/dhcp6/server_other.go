//go:build !linux

package dhcp6

import (
	"context"
	"errors"
	"log/slog"

	"github.com/meister119/leasorix/internal/metrics"
)

// Server is only implemented on Linux.
type Server struct{}

// NewServer returns a server that fails on Run on non-Linux platforms.
func NewServer(*slog.Logger, *Handler, []string, int) *Server { return &Server{} }

// SetMetrics is a no-op.
func (s *Server) SetMetrics(*metrics.DHCP6) {}

// Ready always fails.
func (s *Server) Ready(context.Context) error { return errors.New("DHCPv6 is only supported on Linux") }

// Run fails.
func (s *Server) Run(context.Context) error { return errors.New("DHCPv6 is only supported on Linux") }

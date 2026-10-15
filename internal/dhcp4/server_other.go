//go:build !linux

package dhcp4

import (
	"context"
	"errors"
	"log/slog"
)

// Server is only implemented on Linux.
type Server struct{}

// NewServer returns a server that fails on Run on non-Linux platforms.
func NewServer(*slog.Logger, *Handler, []string, int) *Server { return &Server{} }

// Ready always fails.
func (s *Server) Ready(context.Context) error { return errors.New("dhcpv4 server requires linux") }

// Run always fails.
func (s *Server) Run(context.Context) error { return errors.New("dhcpv4 server requires linux") }

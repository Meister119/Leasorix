//go:build !ui

package web

import "net/http"

// Handler returns nil: this binary was built without the web UI.
func Handler() http.Handler { return nil }

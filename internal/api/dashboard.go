package api

import (
	_ "embed"
	"net/http"
)

//go:embed dashboard.html
var dashboardHTML []byte

// dashboard serves the operator page. It is deliberately a single static file
// with no build step and no external resources: §4.1 binds the API to loopback
// and the offline lane (§6.1) forbids egress, so a page that fetched a CDN
// would simply be blank for the users who need it most.
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'")
	_, _ = w.Write(dashboardHTML)
}

package subpage

import (
	_ "embed"
	"net/http"
)

// The page is a static shell: everything it shows is fetched from the panel by
// the browser, so there is no server-side templating to get wrong and nothing
// personal baked into the HTML.
//
//go:embed page.html
var pageHTML []byte

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	if shortID(r.URL.Path) == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(pageHTML)
}

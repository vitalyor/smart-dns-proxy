package subpage

import (
	"embed"
	_ "embed"
	"net/http"
	"strings"
)

// The page is a static shell: everything it shows is fetched from the panel by
// the browser, so there is no server-side templating to get wrong and nothing
// personal baked into the HTML.
//
//go:embed page.html
var pageHTML []byte

// Шрифты лежат рядом с бинарником, а не тянутся из сети: страницу открывают
// с телефона в дороге, а сторонний источник — это ещё и лишний наблюдатель,
// который узнаёт, кто и когда открыл личную ссылку.
//
//go:embed fonts
var fontsFS embed.FS

func (s *Server) font(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !strings.HasSuffix(name, ".woff2") || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	b, err := fontsFS.ReadFile("fonts/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "font/woff2")
	// Файл неизменен в пределах версии образа — можно кэшировать надолго.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(b)
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	if shortID(r.URL.Path) == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(pageHTML)
}

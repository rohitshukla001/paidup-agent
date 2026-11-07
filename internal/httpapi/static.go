package httpapi

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// static serves the built Angular app from cfg.WebDir. A path that is not
// a file gets index.html, so the app's own routes work on reload. Without
// a build, "/" says that the API runs.
func (s *server) static() http.Handler {
	files := http.FileServer(http.Dir(s.cfg.WebDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		index := filepath.Join(s.cfg.WebDir, "index.html")
		if _, err := os.Stat(index); err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("PaidUp API is running. The web app is not built.\n"))
			return
		}
		clean := path.Clean("/" + r.URL.Path)
		if clean != "/" {
			fi, err := os.Stat(filepath.Join(s.cfg.WebDir,
				filepath.FromSlash(strings.TrimPrefix(clean, "/"))))
			if err == nil && !fi.IsDir() {
				// Angular names its bundles by content hash.
				if strings.Contains(path.Base(clean), ".") &&
					!strings.HasSuffix(clean, ".html") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, index)
	})
}

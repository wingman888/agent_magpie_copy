//go:build dev

package gui

import (
	"bytes"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// Development build (`make dev`): the UI is read from internal/gui/assets on
// every request, and the page reloads itself when a file there changes. Go
// changes still need a rebuild; `make dev` restarts on those too.

func assetsDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "assets")
}

func staticFS() fs.FS { return os.DirFS(assetsDir()) }

// stamp is the newest modification time under assets, as a string.
func stamp() string {
	var newest time.Time
	filepath.WalkDir(assetsDir(), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil && info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
		return nil
	})
	return strconv.FormatInt(newest.UnixNano(), 10)
}

const reloadJS = `(async () => {
  let seen = "";
  for (;;) {
    try {
      const r = await fetch("/api/dev/wait?since=" + seen, { cache: "no-store" });
      const s = (await r.json()).stamp;
      if (seen && s !== seen) { location.reload(); return; }
      seen = s;
    } catch { await new Promise(r => setTimeout(r, 1000)); }
  }
})();`

func devRoutes(mux *http.ServeMux) {
	// long-poll: answers as soon as the assets change, or after 20s
	mux.HandleFunc("GET /api/dev/wait", func(rw http.ResponseWriter, r *http.Request) {
		since := r.URL.Query().Get("since")
		s := stamp()
		for i := 0; i < 50 && since != "" && s == since; i++ {
			time.Sleep(400 * time.Millisecond)
			s = stamp()
		}
		writeJSON(rw, map[string]string{"stamp": s})
	})
	mux.HandleFunc("GET /dev.js", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/javascript")
		rw.Write([]byte(reloadJS))
	})
}

// devPage injects the reload script into index.html and turns off caching.
func devPage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			next.ServeHTTP(rw, r)
			return
		}
		b, err := os.ReadFile(filepath.Join(assetsDir(), "index.html"))
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		b = bytes.Replace(b, []byte("</body>"), []byte(`<script src="/dev.js"></script></body>`), 1)
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		rw.Write(b)
	})
}

// devListen also serves the whole UI over plain HTTP when MAGPIE_DEV_UI names
// an address, so it can be opened in a browser with real devtools. The
// window actions become no-ops there; everything else is the real thing.
func devListen(h http.Handler) {
	addr := os.Getenv("MAGPIE_DEV_UI")
	if addr == "" {
		return
	}
	go func() {
		log.Println("dev ui:", "http://"+addr+"/?theme=dark")
		if err := http.ListenAndServe(addr, h); err != nil {
			log.Println("dev ui:", err)
		}
	}()
}

package api

import (
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultAppDevURL is the renderer's live Vite dev server (`npm run dev:renderer`,
// kept up by the commander-ui launchd/systemd unit). It serves the app under
// /app/ (Vite base), so /app/ proxies to it path-for-path.
const DefaultAppDevURL = "http://127.0.0.1:5173"

// devProbeTTL is how long one dev-server probe answer is trusted.
const devProbeTTL = 2 * time.Second

// appDev serves /app/ from the live dev server while it answers (HMR included:
// httputil.ReverseProxy passes the WebSocket upgrade through), else from the
// built files.
type appDev struct {
	target *url.URL
	proxy  *httputil.ReverseProxy
	files  http.Handler
	client *http.Client
	now    func() time.Time

	mu      sync.Mutex
	checked time.Time
	up      bool
}

// appHandler is the /app/ handler: the dev server at devURL when it answers,
// else dir. devURL "" means built files only.
func appHandler(dir, devURL string) http.Handler {
	files := http.StripPrefix("/app/", appFiles(dir))
	if devURL == "" {
		return files
	}
	target, err := url.Parse(strings.TrimRight(devURL, "/"))
	if err != nil || target.Host == "" {
		return files
	}
	d := &appDev{
		target: target,
		files:  files,
		client: &http.Client{Timeout: 500 * time.Millisecond},
		now:    time.Now,
	}
	d.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host // Vite's allowedHosts checks Host
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
			d.forget()
			d.files.ServeHTTP(w, r) // the dev server died mid-request: the bundle
		},
	}
	return d
}

func (d *appDev) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if d.alive() {
		d.proxy.ServeHTTP(w, r)
		return
	}
	d.files.ServeHTTP(w, r)
}

// alive reports whether a Vite dev server answers at /app/ (its index page
// loads /@vite/client), caching the answer for devProbeTTL.
func (d *appDev) alive() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.checked.IsZero() && d.now().Sub(d.checked) < devProbeTTL {
		return d.up
	}
	d.up = probeViteApp(d.client, d.target.String())
	d.checked = d.now()
	return d.up
}

func (d *appDev) forget() {
	d.mu.Lock()
	d.checked = time.Time{}
	d.mu.Unlock()
}

// probeViteApp is true when base+"/app/" is a Vite dev server's page (and
// not some other program that happens to hold the port).
func probeViteApp(client *http.Client, base string) bool {
	resp, err := client.Get(base + "/app/")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return strings.Contains(string(body), "/@vite/client")
}

package main

import (
	"fmt"
	"html"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"orkestra/internal/config"
	"orkestra/internal/worktree"
)

// runLoginProxy serves the "login sink" role on :3000 for EVERY fe app at
// once, so no main-clone dev server (nor sink swapping) is needed.
//
// Why 3000 matters: Keycloak's client whitelist only accepts
// http://localhost:3000 redirect URIs, and the apps pin NEXTAUTH_URL there,
// so every OAuth hop (signin page, KC callback) lands on port 3000 no
// matter which task port the user started from. Any instance of the same
// app can complete the exchange (localhost cookies are port-agnostic), and
// every worktree fe IS such an instance — so instead of running a
// dedicated app on 3000, forward 3000's traffic to the task port the login
// began on.
//
// Routing: a request arriving with a localhost Referer from a real app
// port pins that port in an "ork_login_target" cookie; later requests
// without a usable Referer (e.g. the KC callback, which comes from
// sso.ebury.rocks) follow the cookie. One login flow at a time — which
// matches the browser's one-session-cookie-at-a-time reality anyway.
func runLoginProxy(listen string) {
	if listen == "" {
		listen = "127.0.0.1:3000"
	}

	taskPort := func(raw string) int {
		u, err := url.Parse(raw)
		if err != nil || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") {
			return 0
		}
		if p, _ := strconv.Atoi(u.Port()); p >= 3001 && p <= 3999 {
			return p
		}
		return 0
	}
	target := func(r *http.Request) int {
		// next-auth's middleware redirect puts the original (task-port) URL
		// in callbackUrl — the most reliable signal, present even on direct
		// navigations where the cross-origin redirect strips the Referer.
		if p := taskPort(r.URL.Query().Get("callbackUrl")); p != 0 {
			return p
		}
		if p := taskPort(r.Referer()); p != 0 {
			return p
		}
		if c, err := r.Cookie("ork_login_target"); err == nil {
			if p, _ := strconv.Atoi(c.Value); p >= 3001 && p <= 3999 {
				return p
			}
		}
		return 0
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			p := target(pr.In)
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = fmt.Sprintf("127.0.0.1:%d", p)
			// Keep the original Host header: the app believes it lives on
			// localhost:3000 (NEXTAUTH_URL) and must keep believing it.
			pr.Out.Host = pr.In.Host
			pr.Out.Header.Set("X-Ork-Target", strconv.Itoa(p))
		},
		// The OAuth callback's final redirect is app-relative (the apps'
		// redirect() callbacks strip origins), which would leave the browser
		// parked on :3000 after login. Rewrite it to the task port so the
		// user lands back where they started.
		ModifyResponse: func(resp *http.Response) error {
			if !strings.HasPrefix(resp.Request.URL.Path, "/api/auth/callback/") {
				return nil
			}
			loc := resp.Header.Get("Location")
			if loc == "" {
				return nil
			}
			p := resp.Request.Header.Get("X-Ork-Target")
			if u, err := url.Parse(loc); err == nil && p != "" &&
				(u.Host == "" || u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1") {
				resp.Header.Set("Location", fmt.Sprintf("http://localhost:%s%s", p, u.RequestURI()))
			}
			// The pin has served its flow — expire it so a later login for a
			// different app family can't silently follow a stale target.
			resp.Header.Add("Set-Cookie", "ork_login_target=; Path=/; Max-Age=0")
			return nil
		},
	}

	// Explicit pin: /__ork/target/<port>?back=<uri> — the chooser's links.
	http.HandleFunc("/__ork/target/", func(w http.ResponseWriter, r *http.Request) {
		p, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/__ork/target/"))
		if p < 3001 || p > 3999 {
			http.Error(w, "bad port", http.StatusBadRequest)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: "ork_login_target", Value: strconv.Itoa(p),
			Path: "/", MaxAge: 600, SameSite: http.SameSiteLaxMode,
		})
		back := r.URL.Query().Get("back")
		if back == "" || !strings.HasPrefix(back, "/") {
			back = "/"
		}
		http.Redirect(w, r, back, http.StatusFound)
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := target(r)
		if p == 0 {
			// Try to figure it out ourselves: scan the worktrees for live fe
			// dev servers. Exactly one alive -> route there; several -> ask.
			live := liveTaskFEs()
			if len(live) == 1 {
				p = live[0].port
			} else {
				serveChooser(w, r, live)
				return
			}
		}
		// Pin/refresh the target so the Referer-less KC callback still routes.
		http.SetCookie(w, &http.Cookie{
			Name: "ork_login_target", Value: strconv.Itoa(p),
			Path: "/", MaxAge: 600, SameSite: http.SameSiteLaxMode,
		})
		fmt.Fprintf(os.Stderr, "%s %s -> :%d\n", r.Method, r.URL.Path, p)
		proxy.ServeHTTP(w, r)
	})

	fmt.Fprintf(os.Stderr, "ork login-proxy on %s — auth traffic follows the port you log in from\n", listen)
	if err := http.ListenAndServe(listen, nil); err != nil {
		fatal("login-proxy: " + err.Error())
	}
}

type liveFE struct {
	repo, task string
	port       int
}

// liveTaskFEs scans the configured worktree roots and reports which tasks
// have something listening on their derived fe port right now.
func liveTaskFEs() []liveFE {
	home, _ := os.UserHomeDir()
	cfg, err := config.Load(filepath.Join(home, ".ork.conf"))
	if err != nil {
		return nil
	}
	var out []liveFE
	seen := map[int]bool{}
	for _, root := range cfg.WorktreeRoots {
		repos, _ := os.ReadDir(root)
		for _, repo := range repos {
			tasks, _ := os.ReadDir(filepath.Join(root, repo.Name()))
			for _, t := range tasks {
				if !t.IsDir() {
					continue
				}
				fe, _ := worktree.TaskPorts(t.Name())
				if seen[fe] {
					continue
				}
				c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", fe), 150*time.Millisecond)
				if err != nil {
					continue
				}
				c.Close()
				seen[fe] = true
				out = append(out, liveFE{repo: repo.Name(), task: t.Name(), port: fe})
			}
		}
	}
	return out
}

const chooserCSS = `
:root{--bg:#14171a;--card:#1d2126;--ink:#e6e9ec;--muted:#8b939c;--accent:#5eead4;--line:#2b3138}
@media (prefers-color-scheme: light){:root{--bg:#f5f6f7;--card:#fff;--ink:#1c2126;--muted:#67707a;--accent:#0d9488;--line:#e3e6e9}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);
font:16px/1.5 ui-sans-serif,system-ui,"Segoe UI",sans-serif;display:grid;place-items:center;min-height:100vh}
main{width:min(30rem,92vw);padding:2rem 0}
h1{font-size:1.05rem;margin:0 0 .25rem;letter-spacing:.01em}
h1 span{color:var(--accent);font-family:ui-monospace,Menlo,Consolas,monospace}
.sub{color:var(--muted);font-size:.9rem;margin:0 0 1.25rem}
a.card{display:flex;align-items:center;gap:.9rem;background:var(--card);border:1px solid var(--line);
border-radius:10px;padding:.85rem 1.1rem;margin-bottom:.6rem;text-decoration:none;color:var(--ink)}
a.card:hover,a.card:focus-visible{border-color:var(--accent);outline:none}
.dot{width:.55rem;height:.55rem;border-radius:50%;background:var(--accent);flex:none}
.task{font-weight:600}
.meta{color:var(--muted);font-size:.82rem;font-family:ui-monospace,Menlo,Consolas,monospace}
.spacer{flex:1}
.port{color:var(--accent);font-family:ui-monospace,Menlo,Consolas,monospace;font-size:.85rem}
.empty{background:var(--card);border:1px dashed var(--line);border-radius:10px;padding:1.1rem;color:var(--muted);font-size:.92rem}
kbd{background:var(--bg);border:1px solid var(--line);border-radius:4px;padding:.05em .4em;font-size:.85em;font-family:ui-monospace,Menlo,monospace}
`

// serveChooser renders the pick-your-task page shown when the target can't
// be inferred (no callbackUrl/Referer/cookie and 0 or 2+ live fe servers).
func serveChooser(w http.ResponseWriter, r *http.Request, live []liveFE) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>ork · login router</title><style>%s</style><main>`, chooserCSS)
	fmt.Fprint(w, `<h1><span>ork</span> login router</h1>`)
	if len(live) == 0 {
		fmt.Fprint(w, `<p class="sub">No task frontends running.</p>`)
		fmt.Fprint(w, `<div class="empty">Spawn one first — <kbd>ctrl-g</kbd> on the task's row in ork — then come back, or just start the login from the task's own page.</div></main>`)
		return
	}
	fmt.Fprint(w, `<p class="sub">Which task is this login for?</p>`)
	back := url.QueryEscape(r.URL.RequestURI())
	for _, l := range live {
		fmt.Fprintf(w, `<a class="card" href="/__ork/target/%d?back=%s"><span class="dot"></span><span><div class="task">%s</div><div class="meta">%s</div></span><span class="spacer"></span><span class="port">:%d</span></a>`,
			l.port, back, html.EscapeString(l.task), html.EscapeString(l.repo), l.port)
	}
	fmt.Fprint(w, `</main>`)
}

// parseListenArg allows "ork login-proxy [port|host:port]".
func parseListenArg(args []string) string {
	if len(args) < 2 {
		return ""
	}
	a := args[1]
	if !strings.Contains(a, ":") {
		return "127.0.0.1:" + a
	}
	return a
}

package main

import (
	"fmt"
	"hash/fnv"
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
		if back == "" || back == "/" || !strings.HasPrefix(back, "/") {
			// No real destination — land on the task's own port, not back on
			// the router (whose root always shows the chooser).
			back = fmt.Sprintf("http://localhost:%d/", p)
		}
		http.Redirect(w, r, back, http.StatusFound)
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// The router's root is always the router — a live pin must never
		// make the chooser unreachable (you couldn't switch apps otherwise).
		if r.URL.Path == "/" {
			serveChooser(w, r, liveTaskFEs())
			return
		}
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
		// Only auth traffic is tunneled — for anything else, bounce the
		// browser to the task's real port instead of mirroring the app on
		// :3000 (a URL bar reading 3000 while showing a task app confuses
		// more than it helps).
		if !strings.HasPrefix(r.URL.Path, "/api/auth/") {
			http.Redirect(w, r, fmt.Sprintf("http://localhost:%d%s", p, r.URL.RequestURI()), http.StatusFound)
			return
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

// chooserCSS: the router page cosplays as the ork TUI — terminal ground,
// monospace grid, the TUI's own repo palette, hover row = the `>` cursor.
const chooserCSS = `
*{box-sizing:border-box}
body{margin:0;background:#101317;color:#d8dde2;min-height:100vh;display:grid;place-items:center;
font:14px/1.7 ui-monospace,"Cascadia Code",Menlo,Consolas,monospace}
main{width:min(46rem,94vw);padding:2rem 0}
.title{color:#f4f6f8;font-weight:700}
.dim{color:#6c7680}
.head{color:#f4f6f8;font-weight:700;white-space:pre}
a.row{display:block;color:inherit;text-decoration:none;white-space:pre;padding:.05rem .25rem;border-radius:3px}
a.row:hover,a.row:focus-visible{background:#31363d;outline:none}
a.row::before{content:"  "}
a.row:hover::before,a.row:focus-visible::before{content:"> "}
.live{color:#87d787}
.port{color:#5fd7d7}
.filter{color:#6c7680;margin:.35rem 0 .8rem}
.wrap{overflow-x:auto}
`

// TUI repoPalette (see internal/ui) mapped from xterm-256 to hex.
var chooserPalette = []string{
	"#00afff", "#ff8700", "#5fff87", "#ff00ff", "#ffd700",
	"#00ffff", "#ff0000", "#af87ff", "#afff00", "#ff87ff",
	"#00d7af", "#d7af00", "#875fff", "#ff875f", "#00ff87",
}

func chooserColor(repo string) string {
	h := fnv.New32a()
	h.Write([]byte(repo))
	return chooserPalette[int(h.Sum32())%len(chooserPalette)]
}

// serveChooser renders the pick-your-task page shown when the target can't
// be inferred (no callbackUrl/Referer/cookie and 0 or 2+ live fe servers).
func serveChooser(w http.ResponseWriter, r *http.Request, live []liveFE) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>ork · login router</title><style>%s</style><main>`, chooserCSS)
	fmt.Fprint(w, `<div class="title">ork login router <span class="dim">— pick the task this login is for (click = pin &amp; continue)</span></div>`)
	fmt.Fprintf(w, `<div class="filter">&gt; <span class="dim">%d/%d</span></div>`, len(live), len(live))
	if len(live) == 0 {
		fmt.Fprint(w, `<div class="dim">no task frontends running — spawn one (ctrl-g on the task's row in ork), or start the login from the task's own page</div></main>`)
		return
	}
	fmt.Fprintf(w, `<div class="wrap"><div class="head">  %-18s %-34s %-6s %s</div>`, "REPO", "TASK", "STATE", "PORT")
	back := url.QueryEscape(r.URL.RequestURI())
	for _, l := range live {
		fmt.Fprintf(w, `<a class="row" href="/__ork/target/%d?back=%s"><span style="color:%s">%-18s</span> %-34s <span class="live">%-6s</span> <span class="port">:%d</span></a>`,
			l.port, back, chooserColor(l.repo),
			html.EscapeString(fmt.Sprintf("%-18s", l.repo)), html.EscapeString(fmt.Sprintf("%-34s", l.task)), "live", l.port)
	}
	fmt.Fprint(w, `</div></main>`)
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

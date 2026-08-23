// Command thoughtmesh is the Thought Mesh server: the REST API over a vault
// of plain markdown files, plus the built PWA, served from one origin as a
// single static binary.
//
// The CLI accepts a `serve` subcommand (also the default with no arguments)
// whose flags override the corresponding environment variables, plus
// `version` and `help`.
package main

import (
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/chinmay28/thought-mesh/server/internal/api"
	"github.com/chinmay28/thought-mesh/server/internal/mesh"
	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

// The release build copies apps/web/dist into webdist/ before `go build`, so
// the binary carries the whole client. In a bare checkout the directory holds
// only a README and the server falls back to serving WEB_DIST from disk.
//
//go:embed all:webdist
var embeddedWeb embed.FS

func main() {
	if err := dispatch(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// Usage already written to stderr by the flag package.
			return
		}
		log.Fatalf("[thoughtmesh] failed to start: %v", err)
	}
}

// dispatch routes the first non-flag argument to a subcommand. With no
// arguments (or a leading flag) it serves, so the bare binary runs the app.
func dispatch(args []string) error {
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "", "serve":
		return serve(args)
	case "version":
		fmt.Printf("thoughtmesh %s\n", api.AppVersion)
		return nil
	case "help":
		printUsage(os.Stdout)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		printUsage(os.Stderr)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// config holds the resolved server settings. Precedence is CLI flag > env var
// > built-in default: each flag defaults to the env-resolved value, so an
// unset flag falls through to the environment.
type config struct {
	host    string
	port    string
	vault   string
	webDist string
}

func serve(args []string) error {
	fset := flag.NewFlagSet("serve", flag.ContinueOnError)
	fset.Usage = func() {
		out := fset.Output()
		fmt.Fprint(out, "Usage: thoughtmesh serve [flags]\n\n"+
			"Start the Thought Mesh server. Flags override the matching environment\n"+
			"variable; an unset flag falls back to the env var, then the default.\n\n"+
			"Flags:\n")
		fset.PrintDefaults()
	}

	var cfg config
	var showVersion bool
	fset.StringVar(&cfg.host, "host", envOr("HOST", "0.0.0.0"), "bind address (env HOST)")
	fset.StringVar(&cfg.port, "port", envOr("PORT", "8788"), "listen port (env PORT)")
	fset.StringVar(&cfg.vault, "vault", envOr("THOUGHTMESH_VAULT", "./data/vault"),
		"directory of markdown notes — the vault (env THOUGHTMESH_VAULT)")
	fset.StringVar(&cfg.webDist, "web-dist", os.Getenv("WEB_DIST"),
		"serve the PWA from this directory, overriding embedded assets (env WEB_DIST)")
	fset.BoolVar(&showVersion, "version", false, "print version and exit")

	if err := fset.Parse(args); err != nil {
		return err
	}
	if showVersion {
		fmt.Printf("thoughtmesh %s\n", api.AppVersion)
		return nil
	}
	if extra := fset.Args(); len(extra) > 0 {
		return fmt.Errorf("unexpected argument %q", extra[0])
	}
	return run(cfg)
}

func run(cfg config) error {
	v, err := vault.Open(cfg.vault)
	if err != nil {
		return fmt.Errorf("open vault: %w", err)
	}
	m := mesh.New(v)
	apiHandler := api.New(v, m)
	handler := withWebClient(apiHandler, cfg.webDist)

	addr := net.JoinHostPort(cfg.host, cfg.port)
	log.Printf("[thoughtmesh] %s listening on http://%s:%s (vault: %s)",
		api.AppVersion, cfg.host, cfg.port, v.Root)
	return http.ListenAndServe(addr, handler)
}

// withWebClient serves the built PWA from the same origin as the API so the
// mobile browser shell behaves like an installed app with no CORS hops. Any
// non-API GET that misses a file returns index.html (SPA fallback), so deep
// links like /notes/journal/2026-08-23.md survive a refresh.
func withWebClient(apiHandler http.Handler, webDist string) http.Handler {
	files, origin := webFiles(webDist)
	if files == nil {
		log.Printf("[thoughtmesh] no web build embedded and no WEB_DIST on disk — API only " +
			"(run the web dev server separately).")
		return apiHandler
	}
	log.Printf("[thoughtmesh] serving web client from %s", origin)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api") {
			apiHandler.ServeHTTP(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if info, err := fs.Stat(files, name); err == nil && !info.IsDir() {
			http.ServeFileFS(w, r, files, name)
			return
		}
		if r.Method == http.MethodGet {
			http.ServeFileFS(w, r, files, "index.html")
			return
		}
		http.NotFound(w, r)
	})
}

// webFiles picks the client asset source: an explicit web-dist directory
// (--web-dist flag or WEB_DIST env) wins, then the assets embedded at build
// time, then the default apps/web/dist of a source checkout.
func webFiles(webDist string) (fs.FS, string) {
	if webDist != "" {
		if hasIndex(os.DirFS(webDist)) {
			return os.DirFS(webDist), webDist
		}
		log.Printf("[thoughtmesh] web-dist %s has no index.html — ignoring", webDist)
	}
	if sub, err := fs.Sub(embeddedWeb, "webdist"); err == nil && hasIndex(sub) {
		return sub, "embedded assets"
	}
	if hasIndex(os.DirFS("apps/web/dist")) {
		return os.DirFS("apps/web/dist"), "apps/web/dist"
	}
	return nil, ""
}

func hasIndex(files fs.FS) bool {
	info, err := fs.Stat(files, "index.html")
	return err == nil && !info.IsDir()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func printUsage(w *os.File) {
	fmt.Fprintf(w, `thoughtmesh %s — interconnected notes over plain markdown files (REST API + PWA)

Usage:
  thoughtmesh [serve] [flags]   start the server (default command)
  thoughtmesh version           print version and exit
  thoughtmesh help              show this help

Run "thoughtmesh serve -h" for the serve flags.
`, api.AppVersion)
}

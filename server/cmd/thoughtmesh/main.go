// Command thoughtmesh is the Thought Mesh server: the REST API over a vault
// of plain markdown files, plus the built PWA, served from one origin as a
// single static binary.
//
// The CLI accepts a `serve` subcommand (also the default with no arguments)
// whose flags override the corresponding environment variables, plus
// `version` and `help`.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chinmay28/thought-mesh/server/internal/api"
	"github.com/chinmay28/thought-mesh/server/internal/cloud"
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
	// Automatic cloud sync. Thought Mesh is self-hosted, so there is no
	// shipped application identity to borrow: each deployment registers its
	// own OAuth app with Dropbox and passes the credentials here. Leave the
	// client id empty and the provider is simply offered as "needs setup" in
	// the UI (it can also be entered there).
	cloudSettings string
	dropbox       cloud.Credentials
	publicURL     string
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
	fset.StringVar(&cfg.port, "port", envOr("PORT", "8881"), "listen port (env PORT)")
	fset.StringVar(&cfg.vault, "vault", envOr("THOUGHTMESH_VAULT", "./data/vault"),
		"directory of markdown notes — the vault (env THOUGHTMESH_VAULT)")
	fset.StringVar(&cfg.webDist, "web-dist", os.Getenv("WEB_DIST"),
		"serve the PWA from this directory, overriding embedded assets (env WEB_DIST)")
	fset.StringVar(&cfg.cloudSettings, "cloud-settings", os.Getenv("THOUGHTMESH_CLOUD_SETTINGS"),
		"cloud sync settings file, holding the OAuth grant — kept OUTSIDE the vault "+
			"(env THOUGHTMESH_CLOUD_SETTINGS; default: thoughtmesh-cloud.json beside the vault)")
	fset.StringVar(&cfg.dropbox.ClientID, "dropbox-client-id",
		os.Getenv("THOUGHTMESH_DROPBOX_CLIENT_ID"),
		"Dropbox OAuth app key, enabling cloud sync to Dropbox (env THOUGHTMESH_DROPBOX_CLIENT_ID)")
	fset.StringVar(&cfg.dropbox.ClientSecret, "dropbox-client-secret",
		os.Getenv("THOUGHTMESH_DROPBOX_CLIENT_SECRET"),
		"Dropbox OAuth app secret; omit for a PKCE-only app (env THOUGHTMESH_DROPBOX_CLIENT_SECRET)")
	fset.StringVar(&cfg.publicURL, "public-url", os.Getenv("THOUGHTMESH_PUBLIC_URL"),
		"origin this server is reached at, used to build the OAuth redirect URI "+
			"(env THOUGHTMESH_PUBLIC_URL; default: the request's own origin)")
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

	// Cloud sync settings live OUTSIDE the vault, deliberately: the file
	// holds OAuth tokens, and the vault is exactly what users copy, sync and
	// version by other means — a token that rode along in it would leak with
	// the first push. Default: beside the vault, not inside it. The sync
	// bookkeeping (what both sides looked like when they last agreed) and the
	// local pre-sync backups keep it company, for the same reason.
	settingsPath := cfg.cloudSettings
	if settingsPath == "" {
		settingsPath = filepath.Join(filepath.Dir(v.Root), "thoughtmesh-cloud.json")
	}
	cloudSvc := cloud.NewService(
		cloud.NewStore(settingsPath),
		cloud.NewStateStore(settingsPath),
		v,
		cloud.NewRegistry(cfg.dropbox, nil, time.Now),
		nil, cfg.publicURL)
	// The scheduler is a background poller over the settings file, so it
	// costs one small read a minute when nothing is configured — cheap enough
	// to always run, and it means enabling a schedule from the UI takes
	// effect without a restart.
	scheduler := &cloud.Scheduler{Service: cloudSvc, Log: log.Default()}
	scheduler.Start(context.Background())
	logCloudProviders(cloudSvc)

	apiHandler := api.New(v, m, cloudSvc)
	handler := withWebClient(apiHandler, cfg.webDist)

	addr := net.JoinHostPort(cfg.host, cfg.port)
	log.Printf("[thoughtmesh] %s listening on http://%s:%s (vault: %s)",
		api.AppVersion, cfg.host, cfg.port, v.Root)
	return http.ListenAndServe(addr, handler)
}

// logCloudProviders says at startup which cloud destinations are usable. Not
// having one isn't a misconfiguration to warn about — setup lives on the Sync
// page now — so the line just says where to go.
func logCloudProviders(svc *cloud.Service) {
	var ready []string
	for _, p := range svc.PublicProviders() {
		if p.Configured == 1 {
			ready = append(ready, p.Name)
		}
	}
	if len(ready) == 0 {
		log.Printf("[thoughtmesh] automatic cloud sync: no provider set up yet " +
			"(Sync page in the app, or pass --dropbox-client-id)")
		return
	}
	log.Printf("[thoughtmesh] automatic cloud sync available via %s; OAuth redirect URI is <origin>%s",
		strings.Join(ready, ", "), cloud.CallbackPath)
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

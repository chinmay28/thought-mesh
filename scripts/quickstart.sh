#!/usr/bin/env bash
#
# Thought Mesh — Linux quick-start installer (Ubuntu / Debian / Raspberry Pi OS).
#
# One command, run as root, installs Thought Mesh as a hardened systemd service:
#
#   curl -fsSL https://raw.githubusercontent.com/chinmay28/thought-mesh/main/scripts/quickstart.sh | sudo bash
#
# Two ways to get the binary — THOUGHTMESH_INSTALL picks one:
#
#   source   (default) clone the repo and build it here. Needs Node and Go at
#            build time (installed automatically if missing); works on any
#            architecture and can track any branch/tag/commit.
#   release  download the prebuilt static binary from a GitHub release. No
#            toolchain, no source tree, no compile — seconds instead of minutes
#            on a Raspberry Pi. Only architectures the release publishes are
#            supported; anything else should use source.
#
#            curl -fsSL …/quickstart.sh | sudo THOUGHTMESH_INSTALL=release bash
#
# Both modes produce the same thing: one static binary with the PWA embedded,
# run by the same systemd unit, with the same vault directory. You can switch
# between them by re-running with a different THOUGHTMESH_INSTALL.
#
# It is deliberately *non-disruptive* and *data-safe* — re-run it any time to
# upgrade in place:
#
#   * Idempotent. Re-running only swaps in newer code; it never touches notes.
#   * The vault (your markdown files) lives at a stable path OUTSIDE the source
#     tree ($DATA_DIR/vault), so cloning, rebuilding, or pulling can never
#     clobber it.
#   * Every upgrade STOPS the service, snapshots the vault to a timestamped
#     tar.gz, THEN swaps code in — so a backup is always taken while quiesced.
#   * The new build is compiled (or the new binary downloaded) while the old
#     version keeps serving. If that fails, the running service is untouched.
#   * After restart we poll /api/health; if the new version is unhealthy we
#     ROLL BACK to the previous commit (source mode) or the previous binary
#     (release mode) and restart. The vault itself is never rewritten by an
#     upgrade, so data needs no restore — the snapshot is belt-and-braces.
#
# The deployed artifact is a single static Go binary that embeds the built PWA.
# Node is only needed at BUILD time (to compile the web client with Vite);
# the running service has no Node, npm, or JS runtime dependency.
#
# Configure via environment variables (all optional):
#
#   THOUGHTMESH_INSTALL   source | release        where the binary comes from (default: source)
#   THOUGHTMESH_REPO      git URL to clone        (default: https://github.com/chinmay28/thought-mesh.git)
#   THOUGHTMESH_REF       branch/tag/commit       (default: main; source mode)
#   THOUGHTMESH_RELEASE   latest | <tag>          release to install (default: latest; release mode)
#   THOUGHTMESH_USER      service system user     (default: thoughtmesh)
#   THOUGHTMESH_PREFIX    install prefix          (default: /opt/thoughtmesh; source → $PREFIX/src)
#   THOUGHTMESH_DATA_DIR  vault + backups dir     (default: /var/lib/thoughtmesh)
#   PORT                  port to listen on       (default: 8881)
#   HOST                  bind address            (default: 0.0.0.0)
#   INSTALL_NODE          auto | never            install Node 22 if missing/old (default: auto; source mode, build-time only)
#   INSTALL_GO            auto | never            install Go if missing/old (default: auto; source mode, build-time only)
#   BACKUP_KEEP           pre-upgrade backups kept (default: 10)
#
set -euo pipefail

# ---------------------------------------------------------------------------
# Logging
# ---------------------------------------------------------------------------
if [ -t 1 ]; then
  C_BLUE=$'\033[1;34m'; C_GREEN=$'\033[1;32m'; C_YELLOW=$'\033[1;33m'
  C_RED=$'\033[1;31m'; C_DIM=$'\033[2m'; C_OFF=$'\033[0m'
else
  C_BLUE=''; C_GREEN=''; C_YELLOW=''; C_RED=''; C_DIM=''; C_OFF=''
fi
log()  { printf '%s==>%s %s\n' "$C_BLUE" "$C_OFF" "$*"; }
ok()   { printf '%s ok %s %s\n' "$C_GREEN" "$C_OFF" "$*"; }
warn() { printf '%swarn%s %s\n' "$C_YELLOW" "$C_OFF" "$*" >&2; }
die()  { printf '%serr %s %s\n' "$C_RED" "$C_OFF" "$*" >&2; exit 1; }
step() { printf '\n%s%s%s\n' "$C_DIM" "$*" "$C_OFF"; }

# ---------------------------------------------------------------------------
# Must be root (system-wide service + dedicated user)
# ---------------------------------------------------------------------------
if [ "$(id -u)" -ne 0 ]; then
  die "Run as root: curl -fsSL .../quickstart.sh | sudo bash   (or: sudo ./scripts/quickstart.sh)"
fi
command -v systemctl >/dev/null 2>&1 || die "systemd is required (no systemctl found)."

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
INSTALL_MODE="${THOUGHTMESH_INSTALL:-source}"
case "$INSTALL_MODE" in
  source | release) ;;
  *) die "THOUGHTMESH_INSTALL must be 'source' or 'release' (got '$INSTALL_MODE')." ;;
esac
THOUGHTMESH_REPO="${THOUGHTMESH_REPO:-https://github.com/chinmay28/thought-mesh.git}"
THOUGHTMESH_REF="${THOUGHTMESH_REF:-main}"
RELEASE_TAG="${THOUGHTMESH_RELEASE:-latest}"
SVC_USER="${THOUGHTMESH_USER:-thoughtmesh}"
PREFIX="${THOUGHTMESH_PREFIX:-/opt/thoughtmesh}"
DATA_DIR="${THOUGHTMESH_DATA_DIR:-/var/lib/thoughtmesh}"
PORT="${PORT:-8881}"
HOST="${HOST:-0.0.0.0}"
INSTALL_NODE="${INSTALL_NODE:-auto}"
INSTALL_GO="${INSTALL_GO:-auto}"
BACKUP_KEEP="${BACKUP_KEEP:-10}"

SRC_DIR="$PREFIX/src"
VAULT_DIR="$DATA_DIR/vault"
BACKUP_DIR="$DATA_DIR/backups"
SERVICE_NAME="thoughtmesh"
UNIT_PATH="/etc/systemd/system/${SERVICE_NAME}.service"
# Minimum Go release that can bootstrap the build; the go directive in
# server/go.mod pins the real toolchain, which Go fetches automatically.
GO_MIN_MINOR=23
GO_INSTALL_VERSION="1.25.0"

# If this script is being run from inside an existing checkout (sudo ./scripts/
# quickstart.sh) rather than piped from curl, build that checkout in place.
# Release mode never builds, so it ignores the surrounding checkout entirely.
SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" >/dev/null 2>&1 && pwd)"
LOCAL_CHECKOUT=""
if [ "$INSTALL_MODE" = source ] && git -C "$SELF_DIR" rev-parse --show-toplevel >/dev/null 2>&1; then
  top="$(git -C "$SELF_DIR" rev-parse --show-toplevel)"
  if [ -f "$top/package.json" ] && grep -q '"name": *"thought-mesh"' "$top/package.json" 2>/dev/null; then
    LOCAL_CHECKOUT="$top"
    SRC_DIR="$top"   # build & serve from where the user already cloned
  fi
fi

if [ "$INSTALL_MODE" = release ]; then
  # No source tree at all: the binary is the whole install.
  SERVER_BIN="$PREFIX/bin/thoughtmesh"
  WORK_DIR="$PREFIX"
else
  SERVER_BIN="$SRC_DIR/server/bin/thoughtmesh"
  WORK_DIR="$SRC_DIR"
fi
WEBDIST_DIR="$SRC_DIR/server/cmd/thoughtmesh/webdist"
# Kept for rollback: the binary the service was running before this run.
PREV_BIN="${SERVER_BIN}.prev"
STAGED_BIN="${SERVER_BIN}.new"

log "Thought Mesh quick start"
printf '  %-10s %s\n' "install"  "$INSTALL_MODE$( [ "$INSTALL_MODE" = release ] && echo " ($RELEASE_TAG)" )"
if [ "$INSTALL_MODE" = release ]; then
  printf '  %-10s %s\n' "binary"  "$SERVER_BIN"
else
  printf '  %-10s %s\n' "source"  "$SRC_DIR"
fi
printf '  %-10s %s\n' "data"     "$DATA_DIR"
printf '  %-10s %s\n' "vault"    "$VAULT_DIR"
printf '  %-10s %s\n' "service"  "${SERVICE_NAME}.service (user: $SVC_USER)"
printf '  %-10s %s\n' "listen"   "http://$HOST:$PORT"

# Run npm/git/go as the service user so the tree stays owned by them, and so the
# build matches the runtime account. Falls back to plain exec before the user exists.
as_svc() {
  if id -u "$SVC_USER" >/dev/null 2>&1; then
    # Build needs devDependencies → make sure NODE_ENV isn't 'production'.
    sudo -u "$SVC_USER" --preserve-env=PATH env -u NODE_ENV "$@"
  else
    env -u NODE_ENV "$@"
  fi
}

# ---------------------------------------------------------------------------
# 1. Prerequisites: git, curl, Node >= 20 (web build) and Go >= 1.23 (server)
# ---------------------------------------------------------------------------
step "[1/7] Prerequisites"

APT=0; command -v apt-get >/dev/null 2>&1 && APT=1
ensure_pkg() {
  command -v "$1" >/dev/null 2>&1 && return 0
  [ "$APT" -eq 1 ] || die "'$1' missing and no apt-get to install it. Install it and re-run."
  log "installing $1…"; apt-get update -y >/dev/null; apt-get install -y "$1" >/dev/null
}
ensure_pkg curl
if [ "$INSTALL_MODE" = release ]; then
  # Nothing is compiled and nothing is cloned: curl (to fetch the release) and
  # sha256sum (to check it) are the whole toolchain.
  command -v sha256sum >/dev/null 2>&1 || ensure_pkg coreutils
  ok "curl present — release mode needs no git, Node or Go"
else

  ensure_pkg git
  ok "git $(git --version | awk '{print $3}'), curl present"

  node_ok=0
  if command -v node >/dev/null 2>&1; then
    major="$(node -p 'process.versions.node.split(".")[0]' 2>/dev/null || echo 0)"
    [ "${major:-0}" -ge 20 ] && node_ok=1
  fi
  if [ "$node_ok" -eq 1 ]; then
    ok "node $(node --version) (build-time only — the PWA compiles with Vite)"
  else
    command -v node >/dev/null 2>&1 \
      && warn "node $(node --version) is too old; the web build needs Node >= 20." \
      || warn "Node.js not found (needed only to build the web client)."
    [ "$INSTALL_NODE" = never ] && die "Install Node >= 20 (https://github.com/nodesource/distributions) and re-run, or set INSTALL_NODE=auto."
    [ "$APT" -eq 1 ] || die "Automatic Node install needs apt. Install Node >= 20 manually and re-run."
    log "installing Node 22 via NodeSource…"
    curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
    apt-get install -y nodejs >/dev/null
    major="$(node -p 'process.versions.node.split(".")[0]')"
    [ "${major:-0}" -ge 20 ] || die "Node install did not yield >= 20 (got $(node --version))."
    ok "node $(node --version) installed"
  fi

  go_ok=0
  GO_BIN="$(command -v go || true)"
  [ -z "$GO_BIN" ] && [ -x /usr/local/go/bin/go ] && GO_BIN=/usr/local/go/bin/go
  if [ -n "$GO_BIN" ]; then
    go_minor="$("$GO_BIN" env GOVERSION 2>/dev/null | sed -E 's/^go1\.([0-9]+).*/\1/' || echo 0)"
    [ "${go_minor:-0}" -ge "$GO_MIN_MINOR" ] && go_ok=1
  fi
  if [ "$go_ok" -eq 1 ]; then
    ok "$("$GO_BIN" version | awk '{print $3}') (newer toolchains fetch automatically per go.mod)"
  else
    [ -n "$GO_BIN" ] \
      && warn "$("$GO_BIN" version 2>/dev/null | awk '{print $3}') is too old; Thought Mesh needs Go >= 1.$GO_MIN_MINOR." \
      || warn "Go not found (needed to build the server binary)."
    [ "$INSTALL_GO" = never ] && die "Install Go >= 1.$GO_MIN_MINOR (https://go.dev/dl) and re-run, or set INSTALL_GO=auto."
    case "$(uname -m)" in
      x86_64)          go_arch=amd64 ;;
      aarch64 | arm64) go_arch=arm64 ;;
      armv6l | armv7l) go_arch=armv6l ;;
      *) die "Unsupported architecture $(uname -m) for automatic Go install; install Go manually and re-run." ;;
    esac
    log "installing Go $GO_INSTALL_VERSION ($go_arch) to /usr/local/go…"
    curl -fsSL "https://go.dev/dl/go${GO_INSTALL_VERSION}.linux-${go_arch}.tar.gz" -o /tmp/thoughtmesh-go.tgz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/thoughtmesh-go.tgz
    rm -f /tmp/thoughtmesh-go.tgz
    GO_BIN=/usr/local/go/bin/go
    ok "$("$GO_BIN" version | awk '{print $3}') installed"
  fi
  GO_DIR="$(dirname "$GO_BIN")"

fi  # end of source-mode prerequisites

# ---------------------------------------------------------------------------
# 2. Dedicated system user (home = data dir, no login shell)
# ---------------------------------------------------------------------------
step "[2/7] Service user '$SVC_USER'"
if id -u "$SVC_USER" >/dev/null 2>&1; then
  ok "user '$SVC_USER' already exists"
else
  nologin="$(command -v nologin || echo /usr/sbin/nologin)"
  useradd --system --home-dir "$DATA_DIR" --create-home --shell "$nologin" "$SVC_USER"
  ok "created system user '$SVC_USER'"
fi

# ---------------------------------------------------------------------------
# 3. The code: a release binary to download, or a source tree to clone/update.
#    Either way the vault is elsewhere and is never touched here.
# ---------------------------------------------------------------------------

# --- release mode: resolve, download and verify the published binary --------

# The architecture name in the asset. Releases publish only what the release
# workflow builds (GOARCHES in .github/workflows/release.yml) — anything else
# has to build from source, which works everywhere.
release_arch() {
  case "$(uname -m)" in
    aarch64 | arm64) echo arm64 ;;
    x86_64 | amd64)  echo amd64 ;;
    *) die "no prebuilt binary for $(uname -m); re-run with THOUGHTMESH_INSTALL=source to build one." ;;
  esac
}

# owner/repo, derived from THOUGHTMESH_REPO so a fork's releases are found too.
release_slug() {
  printf '%s' "$THOUGHTMESH_REPO" | sed -E 's#^.*github\.com[:/]+##; s#\.git$##; s#/+$##'
}

# Download URL for one asset of the requested release.
release_url() {
  local slug; slug="$(release_slug)"
  if [ "$RELEASE_TAG" = latest ]; then
    printf 'https://github.com/%s/releases/latest/download/%s' "$slug" "$1"
  else
    printf 'https://github.com/%s/releases/download/%s/%s' "$slug" "$RELEASE_TAG" "$1"
  fi
}

RELEASE_VERSION=""
fetch_release() {
  local arch asset url tmp
  arch="$(release_arch)"
  asset="thoughtmesh-linux-$arch"
  url="$(release_url "$asset")"

  install -d -m 755 "$PREFIX" "$(dirname "$SERVER_BIN")"
  tmp="$(mktemp -d)"

  log "downloading $asset ($RELEASE_TAG) from $(release_slug)…"
  curl -fSL --progress-bar --retry 3 --retry-delay 2 -o "$tmp/$asset" "$url" \
    || die "could not download $url — no release '$RELEASE_TAG' publishes linux/$arch yet? Re-run with THOUGHTMESH_INSTALL=source."

  # Verify the checksum published beside the binary. A missing .sha256 is a
  # warning (older releases predate it); a mismatch is fatal.
  if curl -fsL --retry 2 -o "$tmp/$asset.sha256" "$url.sha256"; then
    (cd "$tmp" && sha256sum -c "$asset.sha256" >/dev/null 2>&1) \
      || die "checksum mismatch on $asset — refusing to install it."
    ok "sha256 verified"
  else
    warn "no $asset.sha256 published — installing without checksum verification."
  fi

  chmod 755 "$tmp/$asset"
  # Cheapest possible smoke test, and it catches a wrong-architecture download
  # before anything is swapped in: `version` needs no vault and no port.
  RELEASE_VERSION="$("$tmp/$asset" version 2>/dev/null || true)"
  [ -n "$RELEASE_VERSION" ] || die "the downloaded binary does not run on this host (wrong architecture?)."
  mv "$tmp/$asset" "$STAGED_BIN"
  rm -rf "$tmp"
  ok "fetched $RELEASE_VERSION (linux/$arch)"
}

# Swap the staged binary in. `mv` is a rename, so this is safe even while the
# old binary is executing — the running process keeps its own inode.
install_staged() {
  [ -f "$STAGED_BIN" ] || return 0
  if [ -f "$SERVER_BIN" ]; then cp -f "$SERVER_BIN" "$PREV_BIN"; fi
  chown "$SVC_USER":"$SVC_USER" "$STAGED_BIN" 2>/dev/null || true
  chmod 755 "$STAGED_BIN"
  mv -f "$STAGED_BIN" "$SERVER_BIN"
  ok "installed $SERVER_BIN"
}

if [ "$INSTALL_MODE" = release ]; then
  step "[3/7] Release binary ($RELEASE_TAG)"
else
  step "[3/7] Source at $SRC_DIR"
fi

# Detect upgrade BEFORE we change anything, so we know whether to back up / roll back.
UPGRADE=0
{ [ -d "$VAULT_DIR" ] || [ -f "$UNIT_PATH" ]; } && UPGRADE=1

PREV_SHA=""
if [ "$INSTALL_MODE" = release ]; then
  # Downloaded now, installed in step 6 — the old version keeps serving until
  # then, exactly as a source build does while it compiles.
  fetch_release
elif [ -n "$LOCAL_CHECKOUT" ]; then
  warn "building your existing checkout in place (no git fetch)."
  PREV_SHA="$(git -C "$SRC_DIR" rev-parse HEAD 2>/dev/null || true)"
  ok "source at ${PREV_SHA:0:12}"
elif [ -d "$SRC_DIR/.git" ]; then
  PREV_SHA="$(git -C "$SRC_DIR" rev-parse HEAD 2>/dev/null || true)"
  log "updating to $THOUGHTMESH_REF…"
  # A shallow checkout would make every build report patch 0 (the version's
  # patch number is the commit count) — deepen it once if found.
  if [ "$(as_svc git -C "$SRC_DIR" rev-parse --is-shallow-repository 2>/dev/null || echo false)" = true ]; then
    log "deepening shallow checkout (the version's patch number is the commit count)…"
    as_svc git -C "$SRC_DIR" fetch --unshallow --filter=blob:none origin \
      || as_svc git -C "$SRC_DIR" fetch --unshallow origin \
      || warn "could not deepen; this build will report patch 0."
  fi
  as_svc git -C "$SRC_DIR" fetch --filter=blob:none origin "$THOUGHTMESH_REF" \
    || as_svc git -C "$SRC_DIR" fetch origin "$THOUGHTMESH_REF"
  as_svc git -C "$SRC_DIR" checkout -q -B deploy FETCH_HEAD
  ok "updated $( [ -n "$PREV_SHA" ] && echo "${PREV_SHA:0:12} → " )$(git -C "$SRC_DIR" rev-parse --short HEAD)"
else
  log "cloning $THOUGHTMESH_REPO (ref: $THOUGHTMESH_REF)…"
  mkdir -p "$PREFIX"
  # NOT --depth 1: the version's patch number is the commit count, and a
  # shallow clone would make every build call itself patch 1. --filter=blob:none
  # keeps it cheap — the whole commit graph, but only the blobs the checkout
  # actually needs. Fall back to a plain clone if the server or git is too old
  # for partial clone (needs git >= 2.19).
  git clone --filter=blob:none --branch "$THOUGHTMESH_REF" "$THOUGHTMESH_REPO" "$SRC_DIR" \
    || git clone --branch "$THOUGHTMESH_REF" "$THOUGHTMESH_REPO" "$SRC_DIR" \
    || git clone "$THOUGHTMESH_REPO" "$SRC_DIR"
  chown -R "$SVC_USER" "$PREFIX"
  ok "cloned to $SRC_DIR"
fi
if [ "$INSTALL_MODE" = source ]; then
  chown -R "$SVC_USER" "$SRC_DIR" 2>/dev/null || true
  [ -f "$SRC_DIR/package.json" ] || die "no package.json at $SRC_DIR — checkout failed?"
fi

# ---------------------------------------------------------------------------
# 4. Build (server keeps running on the old binary while we compile).
#    Release mode has nothing to build — step 3 already fetched the binary.
# ---------------------------------------------------------------------------
if [ "$INSTALL_MODE" = release ]; then
  step "[4/7] Build — skipped (prebuilt release binary)"
else
  step "[4/7] Build (web → static Go binary)"
fi

build_src() {
  cd "$SRC_DIR"
  if [ -f package-lock.json ]; then as_svc npm ci; else as_svc npm install; fi
  as_svc npm run build --workspace @thoughtmesh/web
  # Embed the built PWA into the server binary: one artifact, one process.
  find "$WEBDIST_DIR" -mindepth 1 ! -name README.txt -delete 2>/dev/null || true
  mkdir -p "$WEBDIST_DIR"
  cp -r "$SRC_DIR/apps/web/dist/." "$WEBDIST_DIR/"
  chown -R "$SVC_USER" "$WEBDIST_DIR" 2>/dev/null || true
  # Version patch number = the commit count (see scripts/version.mjs). Run it
  # as the service user like every other build step: git refuses to read a
  # repo owned by someone else, so asking as root would silently yield 0.
  # Falls back to 0 — the "unstamped build" marker — if it can't be known.
  patch="$(as_svc node "$SRC_DIR/scripts/version.mjs" --patch 2>/dev/null || echo 0)"
  # CGO_ENABLED=0 → fully static binary (no cgo anywhere in the tree).
  as_svc env PATH="$GO_DIR:$PATH" CGO_ENABLED=0 \
    sh -c "cd '$SRC_DIR/server' && go build -trimpath -ldflags '-s -w -X github.com/chinmay28/thought-mesh/server/internal/version.Patch=$patch' -o '$SERVER_BIN' ./cmd/thoughtmesh"
  [ -x "$SERVER_BIN" ] || die "build produced no server binary"
}
if [ "$INSTALL_MODE" = release ]; then
  ok "nothing to build — $RELEASE_VERSION is staged, installed after the backup"
else
  build_src
  ok "build complete → $SERVER_BIN"
fi

# ---------------------------------------------------------------------------
# 5. Data dir + pre-upgrade vault snapshot
# ---------------------------------------------------------------------------
step "[5/7] Vault + backup"
install -d -o "$SVC_USER" -g "$SVC_USER" -m 750 "$DATA_DIR" "$VAULT_DIR" "$BACKUP_DIR"
ok "vault ready ($VAULT_DIR, owned by $SVC_USER)"

stop_service()  { systemctl stop  "${SERVICE_NAME}.service" 2>/dev/null || true; }
start_service() { systemctl start "${SERVICE_NAME}.service"; }

SNAP=""
if [ "$UPGRADE" -eq 1 ] && [ -n "$(ls -A "$VAULT_DIR" 2>/dev/null)" ]; then
  # Quiesce first so the snapshot is consistent (no live writers).
  stop_service
  ts="$(date +%Y%m%d-%H%M%S)"
  SNAP="$BACKUP_DIR/vault-$ts.tar.gz"
  tar -C "$DATA_DIR" -czf "$SNAP" vault
  chown "$SVC_USER":"$SVC_USER" "$SNAP" 2>/dev/null || true
  ok "vault backed up → $SNAP"
  # Prune, keeping the newest $BACKUP_KEEP.
  if [ "$BACKUP_KEEP" -gt 0 ]; then
    ls -1t "$BACKUP_DIR"/vault-*.tar.gz 2>/dev/null | tail -n +"$((BACKUP_KEEP + 1))" | while read -r old; do
      rm -f "$old"
    done
  fi
fi

# ---------------------------------------------------------------------------
# 6. systemd unit + (re)start
# ---------------------------------------------------------------------------
step "[6/7] systemd service"
# The service is quiesced by now on an upgrade, so this is where a downloaded
# binary replaces the running one (keeping the old one for rollback).
install_staged
write_unit() {
  cat > "$UNIT_PATH" <<UNIT
[Unit]
Description=Thought Mesh — interconnected notes over markdown files (REST API + PWA)
Documentation=https://github.com/chinmay28/thought-mesh
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SVC_USER
Group=$SVC_USER
WorkingDirectory=$WORK_DIR
ExecStart=$SERVER_BIN
Environment=THOUGHTMESH_VAULT=$VAULT_DIR
Environment=PORT=$PORT
Environment=HOST=$HOST
Restart=on-failure
RestartSec=3

# Hardening — safe on a trusted LAN, defensive if exposure ever widens.
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=$DATA_DIR

[Install]
WantedBy=multi-user.target
UNIT
}
write_unit
systemctl daemon-reload
systemctl enable "${SERVICE_NAME}.service" >/dev/null 2>&1 || true
start_service
ok "service enabled and started"

# ---------------------------------------------------------------------------
# 7. Health check (with rollback on a failed upgrade)
# ---------------------------------------------------------------------------
step "[7/7] Health check"
health_url="http://127.0.0.1:$PORT/api/health"
check_health() {
  for _ in $(seq 1 30); do
    curl -fsS "$health_url" >/dev/null 2>&1 && return 0
    sleep 0.5
  done
  return 1
}

if check_health; then
  ok "healthy ($health_url) — $(curl -fsS "$health_url" 2>/dev/null | sed -n 's/.*"version" *: *"\([^"]*\)".*/\1/p')"
elif [ "$INSTALL_MODE" = release ] && [ "$UPGRADE" -eq 1 ] && [ -f "$PREV_BIN" ]; then
  # Release-mode rollback: the previous binary is right there — put it back
  # and restart. The vault was never rewritten, so data needs no restore.
  warn "$RELEASE_VERSION failed its health check."
  warn "rolling back to the previous binary…"
  stop_service
  mv -f "$PREV_BIN" "$SERVER_BIN"
  chown "$SVC_USER":"$SVC_USER" "$SERVER_BIN" 2>/dev/null || true
  start_service
  if check_health; then
    die "Upgrade to $RELEASE_VERSION failed its health check — rolled back to $("$SERVER_BIN" version 2>/dev/null || echo "the previous binary") with your notes intact. Check: journalctl -u ${SERVICE_NAME} -n 80"
  fi
  die "Upgrade AND rollback both failed health checks. Vault snapshot is safe at ${SNAP:-$BACKUP_DIR}. Inspect: journalctl -u ${SERVICE_NAME} -n 80"
else
  warn "new version failed its health check."
  if [ "$UPGRADE" -eq 1 ] && [ -n "$PREV_SHA" ] && [ -z "$LOCAL_CHECKOUT" ]; then
    warn "rolling back to ${PREV_SHA:0:12}…"
    stop_service
    as_svc git -C "$SRC_DIR" checkout -q -B deploy "$PREV_SHA"
    build_src
    write_unit
    systemctl daemon-reload
    start_service
    if check_health; then
      die "Upgrade failed health check — rolled back to ${PREV_SHA:0:12} with your notes intact. Check: journalctl -u ${SERVICE_NAME} -n 80"
    fi
    die "Upgrade AND rollback both failed health checks. Vault snapshot is safe at ${SNAP:-$BACKUP_DIR}. Inspect: journalctl -u ${SERVICE_NAME} -n 80"
  fi
  die "Service is not healthy. Inspect logs: journalctl -u ${SERVICE_NAME} -n 80 --no-pager"
fi

# ---------------------------------------------------------------------------
# Done
# ---------------------------------------------------------------------------
lan_ip="$(hostname -I 2>/dev/null | awk '{print $1}')"; [ -n "$lan_ip" ] || lan_ip="<this-host>"
verb="installed"; [ "$UPGRADE" -eq 1 ] && verb="upgraded"

if [ "$INSTALL_MODE" = release ]; then
  origin_line="Installed:   $RELEASE_VERSION, prebuilt from the $RELEASE_TAG release (no toolchain needed)"
  upgrade_line="Upgrade:     re-run with THOUGHTMESH_INSTALL=release for the next release."
else
  origin_line="Source:      $SRC_DIR (built here)"
  upgrade_line="Upgrade:     re-run this script — it swaps code in, backs up notes, self-heals."
fi

cat <<DONE

${C_GREEN}Thought Mesh $verb and running.${C_OFF}

  Open it:     http://$lan_ip:$PORT      (http://localhost:$PORT on this machine)
  Vault:       $VAULT_DIR   (plain markdown files — grep them, sync them, take them anywhere)
  Backups:     $BACKUP_DIR
  Binary:      $SERVER_BIN (static; embeds the web client)
  $origin_line
  $upgrade_line

  Manage the service:
    systemctl status  ${SERVICE_NAME}
    systemctl restart ${SERVICE_NAME}
    journalctl -u ${SERVICE_NAME} -f
${C_DIM}
  No auth by design — keep this on a trusted network (LAN / Tailscale / VPN).
  For HTTPS + "Add to Home Screen", front it with Tailscale Serve or a reverse
  proxy (Caddy/nginx).${C_OFF}
DONE

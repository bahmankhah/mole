package cluster

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// skipPluginDirs are per-machine state or build output, never shipped to nodes.
var skipPluginDirs = map[string]bool{
	".venv": true, "venv": true, "__pycache__": true, "after_crawl_data": true,
	".cookies": true, "node_modules": true, ".git": true,
}

// WritePluginBundle writes the plugins directory as a .tar.gz: the browser
// script, Python requirements and operator plugins with their .env files. A
// joined node is a trusted part of the cluster (it also receives the database
// password), and its plugins need the same settings as on the control node.
func WritePluginBundle(w io.Writer, root string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if skipPluginDirs[name] {
				return filepath.SkipDir
			}
		} else if !d.Type().IsRegular() || strings.HasSuffix(name, ".pyc") || strings.HasSuffix(name, ".log") {
			return nil // symlinks, sockets, compiled and log files
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		}
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// InstallScript returns the node installer served at /cluster/install.sh for
// a control node reachable at controlURL (already validated).
func InstallScript(controlURL string) string {
	return strings.Replace(installScript, "__CONTROL_URL__", shellQuote(controlURL), 1)
}

// JoinCommands returns the commands that add a node with a join token.
func JoinCommands(controlURL, token string) (install, join, docker string) {
	install = "curl -fsSL " + shellQuote(controlURL+"/cluster/install.sh") + " | sudo sh -s -- --token " + shellQuote(token)
	join = "mole join " + shellQuote(controlURL) + " --token " + shellQuote(token)
	docker = "docker run -d --name mole-worker --restart unless-stopped -e MOLE_CONTROL_URL=" + shellQuote(controlURL) +
		" -e MOLE_JOIN_TOKEN=" + shellQuote(token) + " mole:latest worker"
	return install, join, docker
}

const installScript = `#!/bin/sh
# Mole node installer, served by the control node at /cluster/install.sh.
#
#   curl -fsSL <control>/cluster/install.sh | sudo sh -s -- --token <join-token> [options]
#
# Installs the control node's mole binary and plugins into /opt/mole, the
# headless browser (Python venv + Playwright + Chromium), joins the cluster and
# runs the worker as the systemd service mole-worker. Run it again to upgrade
# the node to the control node's current version.
set -eu

CONTROL_URL=__CONTROL_URL__
DIR=/opt/mole
SERVICE_USER=mole
TOKEN=""
NODE_ID=""
REGION=""
MAX_JOBS=""
POOL=""
BROWSER=1
ALL_PLUGINS=0
SERVICE=1
ALLOW_HTTP=0

usage() {
	cat <<'USAGE'
Options:
  --token TOKEN        join token (required; "mole cluster token create" or the System page)
  --id ID              worker id (default: host name plus a random suffix)
  --region REGION      region tag, e.g. ir or intl (default: the token's region)
  --max-jobs N         concurrent crawl jobs on this node
  --browser-pool N     headless browser processes on this node
  --no-browser         do not install the headless browser
  --all-plugins        also install the semantic-search and stemming dependencies (several GB)
  --dir DIR            install directory (default /opt/mole)
  --user USER          system user that runs the worker (default mole)
  --no-service         do not install the systemd service
  --allow-http         allow a plain-http control URL on a public address
USAGE
}

die() { echo "Error: $*" >&2; exit 1; }
warn() { echo "Warning: $*" >&2; }
need() { [ "$2" -ge 2 ] || die "$1 needs a value"; }

while [ $# -gt 0 ]; do
	case "$1" in
		--token) need "$1" $#; TOKEN=$2; shift 2 ;;
		--id) need "$1" $#; NODE_ID=$2; shift 2 ;;
		--region) need "$1" $#; REGION=$2; shift 2 ;;
		--max-jobs) need "$1" $#; MAX_JOBS=$2; shift 2 ;;
		--browser-pool) need "$1" $#; POOL=$2; shift 2 ;;
		--dir) need "$1" $#; DIR=$2; shift 2 ;;
		--user) need "$1" $#; SERVICE_USER=$2; shift 2 ;;
		--no-browser) BROWSER=0; shift ;;
		--all-plugins) ALL_PLUGINS=1; shift ;;
		--no-service) SERVICE=0; shift ;;
		--allow-http) ALLOW_HTTP=1; shift ;;
		-h|--help) usage; exit 0 ;;
		*) usage >&2; die "unknown option: $1" ;;
	esac
done

[ -n "$TOKEN" ] || { usage >&2; die "--token is required"; }
[ "$(id -u)" = 0 ] || die "run as root: curl -fsSL .../cluster/install.sh | sudo sh -s -- --token ..."
[ "$(uname -s)" = Linux ] || die "only Linux is supported"
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"
case "$DIR" in
	/*) ;;
	*) die "--dir must be an absolute path" ;;
esac
case "$DIR" in
	*[!A-Za-z0-9._/-]*) die "--dir may only contain letters, digits, '.', '_', '-' and '/'" ;;
esac
case "$SERVICE_USER" in
	""|*[!a-z0-9_-]*) die "--user may only contain lowercase letters, digits, '_' and '-'" ;;
esac
case "$(uname -m)" in
	x86_64|amd64) ARCH=amd64 ;;
	aarch64|arm64) ARCH=arm64 ;;
	*) die "unsupported CPU architecture $(uname -m)" ;;
esac

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
AUTH="Authorization: Bearer $TOKEN"
mkdir -p "$DIR/bin" "$DIR/data" "$DIR/plugins"

echo "==> Downloading mole from $CONTROL_URL"
curl -fsSL --retry 3 -H "$AUTH" -D "$TMP/headers" -o "$TMP/mole" "$CONTROL_URL/cluster/download/mole" ||
	die "download failed (is the token valid and the control node reachable?)"
REMOTE=$(grep -i '^x-mole-platform:' "$TMP/headers" | tail -n 1 | cut -d: -f2 | tr -d ' \r')
[ "$REMOTE" = "linux/$ARCH" ] ||
	die "the control node's binary is for $REMOTE but this machine is linux/$ARCH; build mole for linux/$ARCH (CGO_ENABLED=0 GOARCH=$ARCH make build), copy it to $DIR/bin/mole and run: $DIR/bin/mole -c $DIR/config.yaml join ..."
chmod 755 "$TMP/mole"
"$TMP/mole" version >/dev/null 2>&1 ||
	die "the downloaded binary does not run on this machine; build the control node's binary with CGO_ENABLED=0 (make build does)"
mv -f "$TMP/mole" "$DIR/bin/mole"
echo "    $("$DIR/bin/mole" version)"

echo "==> Downloading plugins"
curl -fsSL --retry 3 -H "$AUTH" -o "$TMP/plugins.tar.gz" "$CONTROL_URL/cluster/download/plugins.tar.gz" ||
	die "plugin download failed"
tar -xzf "$TMP/plugins.tar.gz" --no-same-owner -C "$DIR/plugins"

install_browser() {
	PY="$DIR/plugins/.venv/bin/python3"
	if [ ! -x "$PY" ]; then
		if ! python3 -m venv "$DIR/plugins/.venv" >/dev/null 2>&1; then
			rm -rf "$DIR/plugins/.venv"
			if command -v apt-get >/dev/null 2>&1; then
				apt-get update -qq || return 1
				DEBIAN_FRONTEND=noninteractive apt-get install -y -qq python3 python3-venv || return 1
			elif command -v dnf >/dev/null 2>&1; then
				dnf install -y -q python3 || return 1
			else
				echo "install python3 with the venv module" >&2
				return 1
			fi
			python3 -m venv "$DIR/plugins/.venv" || return 1
		fi
	fi
	"$PY" -m pip install -q --upgrade pip || return 1
	if [ "$ALL_PLUGINS" = 1 ]; then
		"$PY" -m pip install -q -r "$DIR/plugins/requirements.txt" || return 1
	else
		"$PY" -m pip install -q playwright requests || return 1
	fi
	if ! PLAYWRIGHT_BROWSERS_PATH="$DIR/ms-playwright" "$PY" -m playwright install --with-deps chromium; then
		echo "installing Chromium with its system packages failed; trying the browser alone" >&2
		PLAYWRIGHT_BROWSERS_PATH="$DIR/ms-playwright" "$PY" -m playwright install chromium || return 1
	fi
}

install_documents() {
	if command -v apt-get >/dev/null 2>&1; then
		DEBIAN_FRONTEND=noninteractive apt-get install -y -qq poppler-utils >/dev/null 2>&1 ||
			{ apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq poppler-utils; }
	elif command -v dnf >/dev/null 2>&1; then
		dnf install -y -q poppler-utils
	else
		return 1
	fi
}

if ! command -v pdftotext >/dev/null 2>&1; then
	echo "==> Installing PDF support (poppler-utils)"
	install_documents || warn "poppler-utils could not be installed; PDF pages crawled on this node will not be converted."
fi

if [ "$BROWSER" = 1 ]; then
	echo "==> Installing the headless browser (Python venv, Playwright, Chromium)"
	if ! install_browser; then
		warn "the headless browser could not be installed; this node will only run jobs that do not need it."
		warn "Behind a filtered network, set PIP_INDEX_URL / PLAYWRIGHT_DOWNLOAD_HOST (sudo env ... sh -s -- ...) and run this script again."
	fi
fi

if ! id "$SERVICE_USER" >/dev/null 2>&1; then
	NOLOGIN=$(command -v nologin || echo /bin/false)
	useradd --system --home-dir "$DIR" --shell "$NOLOGIN" "$SERVICE_USER"
fi

echo "==> Joining the cluster"
set -- -c "$DIR/config.yaml" join "$CONTROL_URL"
if [ -n "$NODE_ID" ]; then set -- "$@" --id "$NODE_ID"; fi
if [ -n "$REGION" ]; then set -- "$@" --region "$REGION"; fi
if [ -n "$MAX_JOBS" ]; then set -- "$@" --max-jobs "$MAX_JOBS"; fi
if [ -n "$POOL" ]; then set -- "$@" --browser-pool "$POOL"; fi
if [ "$BROWSER" = 0 ]; then set -- "$@" --no-browser; fi
if [ "$ALLOW_HTTP" = 1 ]; then set -- "$@" --allow-http; fi
(cd "$DIR" && PLAYWRIGHT_BROWSERS_PATH="$DIR/ms-playwright" MOLE_JOIN_TOKEN="$TOKEN" "$DIR/bin/mole" "$@")
chown -R "$SERVICE_USER" "$DIR"

if [ "$SERVICE" = 0 ] || ! command -v systemctl >/dev/null 2>&1 || [ ! -d /run/systemd/system ]; then
	echo ""
	echo "Done. Start the worker with:"
	echo "  cd $DIR && sudo -u $SERVICE_USER env PLAYWRIGHT_BROWSERS_PATH=$DIR/ms-playwright $DIR/bin/mole -c $DIR/config.yaml worker"
	exit 0
fi

cat > /etc/systemd/system/mole-worker.service <<UNIT
[Unit]
Description=Mole crawl worker
Wants=network-online.target
After=network-online.target

[Service]
User=$SERVICE_USER
WorkingDirectory=$DIR
Environment=HOME=$DIR
Environment=PLAYWRIGHT_BROWSERS_PATH=$DIR/ms-playwright
ExecStart=$DIR/bin/mole -c $DIR/config.yaml worker --health-addr 127.0.0.1:8081
Restart=always
RestartSec=10
KillMode=mixed
TimeoutStopSec=90

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable mole-worker >/dev/null 2>&1
systemctl restart mole-worker

echo ""
echo "Done. This node runs as the systemd service mole-worker and appears on the"
echo "control node's System page within 15 seconds."
echo "  status: systemctl status mole-worker"
echo "  logs:   journalctl -u mole-worker -f"
`

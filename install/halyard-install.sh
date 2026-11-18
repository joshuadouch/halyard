#!/usr/bin/env bash
# Halyard installer for Debian 12 (bookworm) and 13 (trixie), in a VM or LXC.
#
# Installs HAProxy 3.x, keepalived, cloudflared and the Halyard service.
#
#   bash halyard-install.sh
#
# Environment (all optional):
#   HALYARD_VERSION   release tag to install (default: latest)
#   HALYARD_REPO      GitHub repo (default: joshuadouch/halyard)
#   GITHUB_TOKEN      token for downloading releases from a private repo
#   HALYARD_BINARY    path to a halyard binary already on this machine (skips download)
#   HALYARD_ROLE      primary (default) or standby
#   HALYARD_SECRET    cluster secret; generate once and reuse on every node
#   HALYARD_LISTEN    management address (default :8443)
#   HALYARD_NO_CLOUDFLARED=1  skip installing cloudflared
set -euo pipefail

REPO="${HALYARD_REPO:-joshuadouch/halyard}"
VERSION="${HALYARD_VERSION:-latest}"
ROLE="${HALYARD_ROLE:-primary}"
LISTEN="${HALYARD_LISTEN:-:8443}"

YW=$'\033[33m'; GN=$'\033[1;92m'; RD=$'\033[01;31m'; BL=$'\033[36m'; CL=$'\033[m'
msg_info() { echo -e " ${YW}…${CL} $1"; }
msg_ok() { echo -e " ${GN}✓${CL} $1"; }
msg_err() { echo -e " ${RD}✗${CL} $1" >&2; }
die() { msg_err "$1"; exit 1; }

[[ $EUID -eq 0 ]] || die "Run as root."
[[ -r /etc/os-release ]] || die "Cannot detect the OS."
# Read os-release in a subshell: it defines VERSION, NAME and others that
# would overwrite this script's variables.
OS_ID="$(. /etc/os-release && echo "${ID:-}")"
OS_NAME="$(. /etc/os-release && echo "${PRETTY_NAME:-unknown}")"
CODENAME="$(. /etc/os-release && echo "${VERSION_CODENAME:-}")"
[[ "$OS_ID" == "debian" ]] || die "Halyard's installer supports Debian 12 or 13 (found ${OS_NAME})."
case "$CODENAME" in bookworm|trixie) ;; *) die "Unsupported Debian release: ${CODENAME:-unknown}";; esac
[[ "$ROLE" == "primary" || "$ROLE" == "standby" ]] || die "HALYARD_ROLE must be primary or standby"

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "Unsupported CPU architecture $(uname -m)";;
esac

export DEBIAN_FRONTEND=noninteractive
# Use a locale that always exists, so apt and perl do not warn over SSH.
export LANG=C.UTF-8 LC_ALL=C.UTF-8

# Fail instead of hanging when a mirror is slow, and stay on IPv4 when this
# machine has an IPv6 address but no IPv6 route out (common on LXC with SLAAC).
cat > /etc/apt/apt.conf.d/90halyard <<'EOF'
Acquire::http::Timeout "30";
Acquire::https::Timeout "30";
Acquire::Retries "3";
EOF
CURL_OPTS=(--connect-timeout 20 --retry 3)
if ! ip -6 route show default 2>/dev/null | grep -q .; then
  echo 'Acquire::ForceIPv4 "true";' >> /etc/apt/apt.conf.d/90halyard
  CURL_OPTS+=(-4)
fi

msg_info "Updating package lists"
apt-get update -q
msg_ok "Package lists updated"
msg_info "Installing base packages"
apt-get install -y -q curl ca-certificates gnupg procps iproute2 keepalived jq
msg_ok "Base packages installed"

msg_info "Installing HAProxy 3.x"
if [[ "$CODENAME" == "bookworm" ]]; then
  curl "${CURL_OPTS[@]}" -fsSL https://haproxy.debian.net/bernat.debian.org.gpg | gpg --dearmor --yes -o /usr/share/keyrings/haproxy.debian.net.gpg
  echo "deb [signed-by=/usr/share/keyrings/haproxy.debian.net.gpg] http://haproxy.debian.net bookworm-backports-3.0 main" \
    > /etc/apt/sources.list.d/haproxy.list
  apt-get update -qq
  apt-get install -y -qq 'haproxy=3.0.*' >/dev/null
else
  apt-get install -y -qq haproxy >/dev/null
fi
msg_ok "Installed $(haproxy -v | head -1 | cut -d' ' -f1-3)"

if [[ "${HALYARD_NO_CLOUDFLARED:-0}" != "1" ]]; then
  msg_info "Installing cloudflared"
  install -d -m 0755 /usr/share/keyrings
  curl "${CURL_OPTS[@]}" -fsSL https://pkg.cloudflare.com/cloudflare-main.gpg -o /usr/share/keyrings/cloudflare-main.gpg
  echo "deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared any main" \
    > /etc/apt/sources.list.d/cloudflared.list
  apt-get update -qq
  apt-get install -y -qq cloudflared >/dev/null
  msg_ok "Installed $(cloudflared --version 2>/dev/null | head -1)"
fi

msg_info "Installing Halyard"
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT
if [[ -n "${HALYARD_BINARY:-}" ]]; then
  [[ -f "$HALYARD_BINARY" ]] || die "HALYARD_BINARY=$HALYARD_BINARY does not exist"
  cp "$HALYARD_BINARY" "$TMP"
else
  ASSET="halyard-linux-${ARCH}"
  if [[ -n "${GITHUB_TOKEN:-}" ]]; then
    # Private repo: resolve the asset through the API.
    API="https://api.github.com/repos/${REPO}/releases/${VERSION}"
    [[ "$VERSION" != "latest" ]] && API="https://api.github.com/repos/${REPO}/releases/tags/${VERSION}"
    ASSET_URL="$(curl "${CURL_OPTS[@]}" -fsSL -H "Authorization: Bearer ${GITHUB_TOKEN}" "$API" | jq -r --arg n "$ASSET" '.assets[] | select(.name==$n) | .url')"
    [[ -n "$ASSET_URL" && "$ASSET_URL" != "null" ]] || die "Release asset $ASSET not found in ${REPO} ${VERSION}"
    curl "${CURL_OPTS[@]}" -fsSL -H "Authorization: Bearer ${GITHUB_TOKEN}" -H "Accept: application/octet-stream" "$ASSET_URL" -o "$TMP"
  else
    URL="https://github.com/${REPO}/releases/latest/download/${ASSET}"
    [[ "$VERSION" != "latest" ]] && URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET}"
    curl "${CURL_OPTS[@]}" -fsSL "$URL" -o "$TMP" || die "Download failed from $URL (private repo? set GITHUB_TOKEN, or copy a binary and set HALYARD_BINARY)"
  fi
fi
UPGRADE=0
OLD_VERSION=""
if [[ -x /usr/local/bin/halyard ]]; then
  UPGRADE=1
  OLD_VERSION="$(/usr/local/bin/halyard version 2>/dev/null || true)"
fi
install -m 0755 "$TMP" /usr/local/bin/halyard
NEW_VERSION="$(/usr/local/bin/halyard version)"
if [[ $UPGRADE == 1 ]]; then
  msg_ok "Updated Halyard ${OLD_VERSION:-unknown} -> ${NEW_VERSION}"
else
  msg_ok "Installed Halyard ${NEW_VERSION}"
fi

# halyard-update re-runs the latest installer from GitHub. The config, database
# and certificates are kept; only packages, the binary and service files change.
cat > /usr/local/bin/halyard-update <<UPD
#!/usr/bin/env bash
# Update Halyard to the latest release, or to HALYARD_VERSION=vX.Y.Z.
set -euo pipefail
export HALYARD_REPO="\${HALYARD_REPO:-${REPO}}"
exec bash -c "\$(curl -fsSL "https://raw.githubusercontent.com/\${HALYARD_REPO}/main/install/halyard-install.sh")"
UPD
chmod 0755 /usr/local/bin/halyard-update
# `pct exec` runs with a minimal PATH (/bin:/usr/bin), so link both commands there.
ln -sf /usr/local/bin/halyard-update /usr/bin/halyard-update
ln -sf /usr/local/bin/halyard /usr/bin/halyard

msg_info "Configuring the system"
install -d -m 0750 /etc/halyard /var/lib/halyard
install -d -m 0755 /var/lib/haproxy /run/haproxy
cat > /etc/sysctl.d/90-halyard.conf <<'EOF'
# Let HAProxy bind VIPs that keepalived has not assigned to this node yet.
net.ipv4.ip_nonlocal_bind = 1
net.ipv6.ip_nonlocal_bind = 1
EOF
sysctl -q -p /etc/sysctl.d/90-halyard.conf 2>/dev/null || echo "   (could not apply sysctls now; they apply on boot)"

if [[ ! -f /etc/halyard/halyard.yaml ]]; then
  NODE_IP="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '/src/ {for (i=1;i<=NF;i++) if ($i=="src") print $(i+1)}' | head -1)"
  SECRET="${HALYARD_SECRET:-$(head -c 24 /dev/urandom | base64 | tr -d '/+=' )}"
  cat > /etc/halyard/halyard.yaml <<EOF
# Halyard node configuration. Hosts, pools and VIPs live in the database and
# are edited in the web UI; this file only describes this node.
listen: "${LISTEN}"
data_dir: /var/lib/halyard

node:
  name: $(hostname -s)
  address: ${NODE_IP:-}
  role: ${ROLE}

cluster:
  # Use the same secret on every node. It protects config sync and VRRP.
  secret: "${SECRET}"
  peers: []
  # peers:
  #   - name: lb-02
  #     address: 10.0.30.12
  #     url: https://10.0.30.12:8443

haproxy:
  binary: /usr/sbin/haproxy
  config_path: /etc/haproxy/haproxy.cfg
  socket: /run/haproxy/admin.sock
  reload_cmd: systemctl reload haproxy
  state_file: /var/lib/haproxy/server-state
  stats_listen: 127.0.0.1:8404

keepalived:
  enabled: true
  config_path: /etc/keepalived/keepalived.conf
  reload_cmd: systemctl reload-or-restart keepalived

cloudflared:
  enabled: $([[ "${HALYARD_NO_CLOUDFLARED:-0}" == "1" ]] && echo false || echo true)
  env_file: /etc/halyard/cloudflared.env
  reload_cmd: systemctl restart cloudflared-halyard
EOF
  chmod 0640 /etc/halyard/halyard.yaml
fi

# keepalived refuses to start without a config file.
[[ -f /etc/keepalived/keepalived.conf ]] || printf 'global_defs {\n    router_id %s\n}\n' "$(hostname -s)" > /etc/keepalived/keepalived.conf

cat > /etc/systemd/system/halyard.service <<'EOF'
[Unit]
Description=Halyard load balancer manager
Documentation=https://github.com/joshuadouch/halyard
After=network-online.target haproxy.service keepalived.service
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/halyard serve -config /etc/halyard/halyard.yaml
Restart=on-failure
RestartSec=3
# Halyard writes HAProxy/keepalived config and reloads them, so it runs as root.
UMask=0027
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/cloudflared-halyard.service <<'EOF'
[Unit]
Description=Cloudflare Tunnel connector managed by Halyard
After=network-online.target
Wants=network-online.target
ConditionPathExists=/etc/halyard/cloudflared.env

[Service]
EnvironmentFile=/etc/halyard/cloudflared.env
ExecStart=/usr/bin/cloudflared --no-autoupdate tunnel run
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

# HAProxy must keep running when a reload is rejected; the Debian unit already
# reloads in master-worker mode, which is hitless.
mkdir -p /etc/systemd/system/haproxy.service.d
cat > /etc/systemd/system/haproxy.service.d/halyard.conf <<'EOF'
[Service]
RuntimeDirectory=haproxy
RuntimeDirectoryPreserve=yes
EOF
systemctl daemon-reload
msg_ok "System configured"

msg_info "Starting services"
systemctl enable -q --now keepalived haproxy
systemctl enable -q cloudflared-halyard 2>/dev/null || true
systemctl enable -q --now halyard
if [[ $UPGRADE == 1 ]]; then systemctl restart halyard; fi
sleep 2
systemctl is-active -q halyard || { journalctl -u halyard -n 30 --no-pager; die "Halyard failed to start"; }
msg_ok "Halyard is running"

IP="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i=1;i<=NF;i++) if ($i=="src") print $(i+1)}' | head -1)"
PORT="${LISTEN##*:}"
echo
if [[ $UPGRADE == 1 ]]; then
  echo -e " ${GN}Halyard ${NEW_VERSION} is running.${CL} Hosts, pools, VIPs and settings were kept."
  echo
  exit 0
fi
echo -e " ${GN}Halyard is ready.${CL}"
echo -e "   Open ${BL}https://${IP:-<this-machine>}:${PORT}${CL} and create the first admin."
echo -e "   The UI uses a self-signed certificate until you put it behind Cloudflare Access."
echo -e "   To update later, run ${BL}halyard-update${CL} on this machine."
if [[ "$ROLE" == "standby" ]]; then
  echo -e "   This is a ${YW}standby${CL}: add it to cluster.peers on the primary and restart halyard there."
fi
echo

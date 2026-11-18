#!/usr/bin/env bash
# Halyard: Proxmox VE helper script.
# Run on a Proxmox VE node shell. Creates a Debian 12 LXC and installs Halyard
# (HAProxy 3.x, keepalived, cloudflared) inside it.
#
#   bash -c "$(curl -fsSL https://raw.githubusercontent.com/joshuadouch/halyard/main/ct/halyard.sh)"
#
# Every prompt can be preset with an environment variable for unattended use:
#   CTID HOSTNAME DISK_SIZE CORE_COUNT RAM_SIZE BRIDGE NET (dhcp or CIDR) GATE
#   EXTRA_BRIDGES (e.g. "vmbr20,vmbr30" for VIP networks) STORAGE TEMPLATE_STORAGE
#   HALYARD_ROLE HALYARD_SECRET HALYARD_VERSION HALYARD_BINARY GITHUB_TOKEN
#   UNATTENDED=1 (accept defaults without prompts)
set -euo pipefail

APP="Halyard"
REPO="${HALYARD_REPO:-joshuadouch/halyard}"
BRANCH="${HALYARD_BRANCH:-main}"

YW=$'\033[33m'; GN=$'\033[1;92m'; RD=$'\033[01;31m'; BL=$'\033[36m'; CL=$'\033[m'; BOLD=$'\033[1m'
msg_info() { echo -e " ${YW}…${CL} $1"; }
msg_ok() { echo -e " ${GN}✓${CL} $1"; }
die() { echo -e " ${RD}✗${CL} $1" >&2; exit 1; }

header() {
  clear || true
  cat <<'EOF'
    __  __      __                    __
   / / / /___ _/ /_  ______ __________/ /
  / /_/ / __ `/ / / / / __ `/ ___/ __  /
 / __  / /_/ / / /_/ / /_/ / /  / /_/ /
/_/ /_/\__,_/_/\__, /\__,_/_/   \__,_/
              /____/   load balancer for Proxmox
EOF
  echo
}

command -v pveversion >/dev/null || die "Run this on a Proxmox VE host."
[[ $EUID -eq 0 ]] || die "Run as root."
header

ask() { # ask VAR "Question" default
  local var="$1" q="$2" def="$3" val
  if [[ -n "${!var:-}" ]]; then return; fi
  if [[ "${UNATTENDED:-0}" == "1" ]] || ! command -v whiptail >/dev/null; then
    printf -v "$var" '%s' "$def"; return
  fi
  val=$(whiptail --backtitle "Halyard" --title "$APP LXC" --inputbox "$q" 9 64 "$def" 3>&1 1>&2 2>&3) || die "Cancelled"
  printf -v "$var" '%s' "$val"
}

ask CTID "Container ID" "$(pvesh get /cluster/nextid)"
ask HOSTNAME "Hostname" "halyard"
ask CORE_COUNT "CPU cores" "1"
ask RAM_SIZE "Memory (MiB)" "1024"
ask DISK_SIZE "Disk size (GiB)" "4"
ask BRIDGE "Management bridge" "vmbr0"
ask NET "Management IPv4 (CIDR like 10.0.10.5/24, or dhcp)" "dhcp"
if [[ "$NET" != "dhcp" ]]; then ask GATE "Gateway" ""; fi
ask EXTRA_BRIDGES "Extra bridges for VIP networks, comma separated (blank for none)" ""
ask HALYARD_ROLE "Role: primary, or standby for a second node" "primary"
ask STORAGE "Storage for the root disk" "$(pvesm status -content rootdir | awk 'NR==2{print $1}')"
ask TEMPLATE_STORAGE "Storage for templates" "$(pvesm status -content vztmpl | awk 'NR==2{print $1}')"

[[ "$CTID" =~ ^[0-9]+$ ]] || die "Invalid container ID"
pct status "$CTID" >/dev/null 2>&1 && die "Container $CTID already exists"
[[ -n "$STORAGE" && -n "$TEMPLATE_STORAGE" ]] || die "No suitable storage found"

msg_info "Finding the Debian 12 template"
pveam update >/dev/null 2>&1 || true
TEMPLATE="$(pveam available --section system | awk '/debian-12-standard/ {print $2}' | sort -V | tail -1)"
[[ -n "$TEMPLATE" ]] || die "No debian-12-standard template available"
if ! pveam list "$TEMPLATE_STORAGE" | grep -q "$TEMPLATE"; then
  pveam download "$TEMPLATE_STORAGE" "$TEMPLATE" >/dev/null
fi
msg_ok "Template $TEMPLATE"

NET0="name=eth0,bridge=${BRIDGE},ip=${NET}"
[[ "$NET" != "dhcp" && -n "${GATE:-}" ]] && NET0+=",gw=${GATE}"
NETS=(--net0 "$NET0")
i=1
IFS=',' read -ra XB <<<"${EXTRA_BRIDGES:-}"
for b in "${XB[@]}"; do
  b="$(echo "$b" | xargs)"; [[ -z "$b" ]] && continue
  NETS+=("--net$i" "name=eth$i,bridge=$b,ip=manual")
  i=$((i + 1))
done

msg_info "Creating LXC $CTID"
pct create "$CTID" "${TEMPLATE_STORAGE}:vztmpl/${TEMPLATE}" \
  --hostname "$HOSTNAME" --cores "$CORE_COUNT" --memory "$RAM_SIZE" --swap 512 \
  --rootfs "${STORAGE}:${DISK_SIZE}" "${NETS[@]}" \
  --unprivileged 1 --features nesting=1 --onboot 1 --tags halyard \
  --description "Halyard load balancer (HAProxy + keepalived). https://github.com/${REPO}" >/dev/null
msg_ok "Created LXC $CTID"

msg_info "Starting container and waiting for network"
pct start "$CTID"
for _ in $(seq 1 30); do
  if pct exec "$CTID" -- getent hosts deb.debian.org >/dev/null 2>&1; then break; fi
  sleep 2
done
pct exec "$CTID" -- getent hosts deb.debian.org >/dev/null 2>&1 || die "Container has no working network/DNS"
msg_ok "Network is up"

# Locate the installer: next to this script when run from a clone, else GitHub.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || echo /nonexistent)"
INSTALLER="$(mktemp)"
trap 'rm -f "$INSTALLER"' EXIT
if [[ -f "$SCRIPT_DIR/../install/halyard-install.sh" ]]; then
  cp "$SCRIPT_DIR/../install/halyard-install.sh" "$INSTALLER"
else
  HDR=()
  [[ -n "${GITHUB_TOKEN:-}" ]] && HDR=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
  curl -fsSL "${HDR[@]}" "https://raw.githubusercontent.com/${REPO}/${BRANCH}/install/halyard-install.sh" -o "$INSTALLER" \
    || die "Could not download the installer (private repo? set GITHUB_TOKEN)"
fi
pct exec "$CTID" -- bash -c "apt-get update -qq && apt-get install -y -qq curl >/dev/null"
pct push "$CTID" "$INSTALLER" /root/halyard-install.sh --perms 0755
ENVS=("HALYARD_ROLE=${HALYARD_ROLE}" "HALYARD_REPO=${REPO}")
[[ -n "${HALYARD_SECRET:-}" ]] && ENVS+=("HALYARD_SECRET=${HALYARD_SECRET}")
[[ -n "${HALYARD_VERSION:-}" ]] && ENVS+=("HALYARD_VERSION=${HALYARD_VERSION}")
[[ -n "${GITHUB_TOKEN:-}" ]] && ENVS+=("GITHUB_TOKEN=${GITHUB_TOKEN}")
if [[ -n "${HALYARD_BINARY:-}" ]]; then
  pct push "$CTID" "$HALYARD_BINARY" /root/halyard --perms 0755
  ENVS+=("HALYARD_BINARY=/root/halyard")
fi

msg_info "Installing Halyard inside the container (this takes a minute or two)"
pct exec "$CTID" -- env "${ENVS[@]}" bash /root/halyard-install.sh
pct exec "$CTID" -- rm -f /root/halyard-install.sh /root/halyard

IP="$(pct exec "$CTID" -- ip -4 -o addr show dev eth0 | awk '{print $4}' | cut -d/ -f1 | head -1)"
echo
echo -e " ${GN}${BOLD}Halyard is installed in LXC ${CTID}.${CL}"
echo -e "   Web UI: ${BL}https://${IP}:8443${CL}"
echo
echo -e "   Update later: ${BL}pct exec ${CTID} -- halyard-update${CL} (or run halyard-update inside the CT)"
echo
echo -e " ${YW}Before adding VIPs:${CL}"
echo -e "   If the Proxmox firewall has IP filter enabled on this container's NICs, add each"
echo -e "   VIP to the container's IP set (Datacenter or CT → Firewall → IPSet ipfilter-net0)."
if [[ "$HALYARD_ROLE" == "primary" ]]; then
  echo -e "   For a second node, run this script again with HALYARD_ROLE=standby and the same"
  echo -e "   HALYARD_SECRET (see cluster.secret in /etc/halyard/halyard.yaml inside CT ${CTID})."
fi
echo

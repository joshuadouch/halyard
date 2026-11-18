# Halyard

A small load balancer for a home lab or small site, built to replace Nginx Proxy
Manager. Halyard is a single Go binary with a web UI. It drives
**HAProxy 3.x** for traffic, **keepalived** for floating VIPs, and talks to
**Cloudflare** for DNS, Tunnels, Authenticated Origin Pulls, Access and
certificates.

- **Hosts** map one or more hostnames (wildcards allowed) on a VIP to pools,
  with optional path routes, HTTPS redirect, HSTS and an allow list.
- **Pools** have several backends with round robin, least connections, source
  hash, random or first; sticky sessions by cookie or source IP; HTTP or TCP
  health checks; slow start; backup members; and live **drain** and
  **maintenance** without a reload.
- **VIPs** are floating IPs. Each one is its own VRRP instance, so different
  hostnames can live on different IPs and each can prefer a different node.
- **Access lists** restrict hosts to allowed client IPs, a basic auth login, or
  both (either one, or both required). One list can be shared by many hosts.
- **TCP hosts** pass any TCP service (databases, SSH, MQTT) through a VIP.
- **Save means apply.** Every change is rendered, checked with `haproxy -c`, and
  reloaded without dropping connections. A config HAProxy rejects is rolled back
  and nothing changes. Every apply is kept in history with a diff and can be
  rolled back.
- **Two nodes.** The primary pushes its configuration (encrypted) to standbys.
  keepalived moves VIPs when a node or HAProxy fails.

## Install on Proxmox VE

Run this in the Proxmox node shell. It creates an unprivileged Debian 12 LXC and
installs everything inside it.

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/joshuadouch/halyard/main/ct/halyard.sh)"
```

While the repository is private, download with a token and pass it through so
the installer can fetch the release binary:

```bash
export GITHUB_TOKEN=github_pat_...
bash -c "$(curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" \
  https://raw.githubusercontent.com/joshuadouch/halyard/main/ct/halyard.sh)"
```

The script asks for the container ID, size, management bridge and address, and
**extra bridges for VIP networks**. Each extra bridge becomes `eth1`, `eth2`…
with no address, ready for VIPs. It also asks for an optional root password and SSH key file
(your node's `/root/.ssh/authorized_keys` is offered by default). With no
password, the Proxmox console logs in as root automatically; `pct enter <CTID>`
always works from the node. Every prompt can be preset with an environment
variable (see the top of `ct/halyard.sh`), and `UNATTENDED=1` accepts defaults.

Open `https://<container-ip>:8443` and create the first admin straight away:
until an admin exists, anyone who can reach the port can claim it.

**Proxmox firewall:** if IP filter is on for the container's NICs, add each VIP
to the container's `ipfilter-net*` IP set, or VRRP will move the address but no
traffic will reach it.

### Any Debian 12 or 13 VM or LXC

```bash
curl -fsSLO https://raw.githubusercontent.com/joshuadouch/halyard/main/install/halyard-install.sh
bash halyard-install.sh
```

Options are environment variables: `HALYARD_VERSION`, `HALYARD_ROLE`
(`primary` or `standby`), `HALYARD_SECRET`, `HALYARD_LISTEN`, `GITHUB_TOKEN`,
`HALYARD_BINARY` (use a binary you already copied) and
`HALYARD_NO_CLOUDFLARED=1`.

## Updating

Every change merged to `main` publishes a new release automatically
(`v0.1.<build>`). To update a node, run this on the Proxmox host:

```bash
pct exec <CTID> -- halyard-update
```

Or run `halyard-update` in the container's shell. It downloads the latest
installer and binary, keeps your config, database and certificates, and restarts
Halyard. HAProxy keeps serving traffic throughout. To pin or roll back a version,
run `HALYARD_VERSION=v0.1.7 halyard-update`.

With two nodes, update the standby first, then the primary.

## Two nodes

1. Install the primary. Its cluster secret is `cluster.secret` in
   `/etc/halyard/halyard.yaml`.
2. Install the second node with `HALYARD_ROLE=standby HALYARD_SECRET=<same secret>`.
3. On both nodes, list the other under `cluster.peers` and restart halyard:

   ```yaml
   cluster:
     secret: "..."
     peers:
       - name: lb-02
         address: 10.0.30.12
         url: https://10.0.30.12:8443
   ```

Create the admin account on the primary only. Users, passwords and 2FA sync to
the standby along with the config (on every change and every 5 minutes), so the
same logins work on both nodes. Make all changes on the primary. The standby's UI is read only and shows sync
status on the Nodes page. Each VIP picks a preferred node; with preempt off a
VIP stays where it is after a failover until that node fails.

## Cloudflare

Paste an API token on the Cloudflare page. Create a custom token with:

| Scope | Permission | Used for |
|---|---|---|
| Zone · Zone | Read | finding the zone for each hostname |
| Zone · DNS | Edit | publishing records |
| Zone · Zone Settings | Edit | turning on Authenticated Origin Pulls |
| Account · Cloudflare Tunnel | Edit | the managed tunnel and its routes |
| Account · Access: Apps and Policies | Edit | protecting the management UI |

Each host has an **exposure**:

- **LAN**: optionally publishes an unproxied A record pointing at the VIP.
- **Public**: publishes a proxied record at your public IP (port forward 443 to
  the VIP).
- **Tunnel**: adds a route on Halyard's Cloudflare Tunnel (run by the
  `cloudflared-halyard` service) and a proxied CNAME to it. No port forward.

Halyard only changes DNS records it created (they carry the comment
`managed by halyard`). An existing record with the same name is shown as a
conflict and left alone.

**Real client IP.** Requests from Cloudflare's edge ranges (refreshed daily) or
from the tunnel on the node itself have the client IP restored from
`CF-Connecting-IP`. Allow lists and logs use it. The header is ignored from
anyone else.

### Authenticated Origin Pulls

Turn on AOP for a zone on the Cloudflare page, then tick **Require AOP** on
public hosts. Halyard downloads Cloudflare's origin-pull CA, asks for a client
certificate only on those hostnames (per SNI, so LAN clients on other names are
unaffected), and returns 403 to any request that did not come through
Cloudflare. Tunnel hosts do not need it: their traffic never leaves the node.

### Certificates

Request certificates on the Certificates page (Let's Encrypt with a Cloudflare
DNS-01 challenge, so wildcards work and nothing has to be reachable) or upload
your own. Certificates renew 30 days before expiry. Each HTTPS host picks the
most specific certificate that covers its name.

## Management UI access

The UI listens on `:8443` with a self-signed certificate.

- **Internal:** sign in with a username and password, with optional TOTP
  two-factor.
- **Cloudflare Access:** set a UI hostname and your Access team on the
  Cloudflare page and press **Create Access app**. Halyard creates the Access
  application and policy, routes the hostname through the tunnel, and trusts the
  signed `Cf-Access-Jwt-Assertion` it receives. People sign in with your Access
  policy; their email must match a Halyard user or be in the allowed emails
  list (users are then created on first sign-in).

Both work at the same time.

## Files

| Path | What |
|---|---|
| `/etc/halyard/halyard.yaml` | node settings (listen address, role, peers, paths) |
| `/var/lib/halyard/halyard.db` | hosts, pools, VIPs, certificates, users, history |
| `/var/lib/halyard/haproxy/` | crt-lists, Cloudflare ranges, origin-pull CA |
| `/var/lib/halyard/certs/` | certificate bundles for HAProxy |
| `/etc/haproxy/haproxy.cfg` | generated; do not edit by hand |
| `/etc/keepalived/keepalived.conf` | generated; do not edit by hand |

`halyard render` prints the configuration the current state would produce.
`halyard user add <name>` and `halyard user passwd <name>` manage users from the
shell, for example if you are locked out.

## Building

Requires Go 1.26 and Node 22.

```bash
make build        # web UI + bin/halyard
make test         # go vet + go test (render tests run haproxy -c when installed)
make release      # dist/halyard-linux-{amd64,arm64}
```

For UI work, run `halyard serve` and `cd web && npm run dev`; Vite proxies `/api`
to `https://127.0.0.1:8443`.

Each merge to `main` that touches the app builds both binaries and publishes a
GitHub release, which the installer downloads. The base version is in `VERSION`.

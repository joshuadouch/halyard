export type Exposure = 'lan' | 'public' | 'tunnel';
export type Mode = 'http' | 'tcp';

export interface VIP {
  id: string; address: string; label: string; interface: string; vrid: number;
  preferred_node: string; preempt: boolean; track_haproxy: boolean;
}
export interface VIPRow extends VIP { hosts: string[]; local: boolean }

export interface Route { path_prefix: string; pool_id: string }
export interface Host {
  id: string; hostnames: string[]; vip_id: string; mode: Mode; http_port: number; https_port: number;
  tcp_port: number; force_https: boolean; hsts: boolean; certificate_id: string; routes: Route[];
  exposure: Exposure; publish_dns: boolean; require_aop: boolean; allow_cidrs: string[] | null;
  enabled: boolean; notes: string;
}
export interface Member { name: string; address: string; weight: number; backup: boolean; state: 'ready' | 'drain' | 'maint' }
export interface HealthCheck {
  enabled: boolean; type: string; method: string; path: string; host_header: string; expect_status: string;
  interval_sec: number; fall: number; rise: number;
}
export interface Pool {
  id: string; name: string; mode: Mode; algorithm: string; sticky: string; upstream_tls: boolean; verify_tls: boolean;
  slow_start_sec: number; retries: number; health: HealthCheck; members: Member[]; timeout_server_sec: number;
}
export interface Cert {
  id: string; domains: string[]; source: string; key_type: string; not_after: string; issuer: string;
  status: string; last_error: string; updated_at: string; hosts: number; issuing: boolean;
}
export interface Stat {
  proxy: string; server: string; status: string; weight: number; cur_sessions: number; tot_sessions: number;
  rate: number; req_rate: number; check_status: string; check_code: string; check_ms: number; last_change_sec: number;
  resp_5xx: number; resp_total: number; avg_resp_ms: number; avg_total_ms: number;
}
export interface PoolStatus {
  id: string; name: string; up: number; total: number; rate: number; sessions: number; resp_5xx: number;
  resp_total: number; avg_ms: number; members: Stat[]; status: string;
}
export interface Event { id: number; time: string; level: string; kind: string; message: string }
export interface Revision { id: number; time: string; author: string; message: string; status: string; error: string; haproxy?: string; keepalived?: string }
export interface User { id: string; username: string; email: string; totp_enabled: boolean }

export interface CFSettings {
  api_token?: string; account_id: string; tunnel_id: string; tunnel_name: string; public_ip: string; acme_email: string;
  acme_staging: boolean; real_ip: boolean; aop_zones: string[] | null; ui_hostname: string; access_team: string;
  access_aud: string; access_app_id: string; access_emails: string[] | null; last_reconcile: string;
}
export interface DNSRecord { id?: string; type: string; name: string; content: string; proxied: boolean }
export interface CFChange { action: string; zone: string; record: string; detail: string }
export interface CFRecordState { zone: string; desired: DNSRecord; actual: DNSRecord | null; status: string }
export interface IngressRule { hostname?: string; service: string; originRequest?: { originServerName?: string; noTLSVerify?: boolean } }
export interface CFReport {
  time: string; dry_run: boolean; changes: CFChange[] | null; records: CFRecordState[] | null;
  ingress: IngressRule[] | null; notes: string[] | null; error: string;
}
export interface Tunnel { id: string; name: string; status: string; connections: { colo_name: string; origin_ip: string; client_version: string }[] | null }
export interface CFView extends CFSettings {
  token_set: boolean; zones: { id: string; name: string; account: { id: string; name: string } }[] | null;
  tunnel: Tunnel | null; report: CFReport; error: string; aop: Record<string, boolean>;
}

export class ApiError extends Error {
  status: number; output?: string; data: any;
  constructor(status: number, message: string, data: any) {
    super(message); this.status = status; this.output = data?.output; this.data = data;
  }
}

let onUnauthorized: () => void = () => {};
export function setUnauthorizedHandler(f: () => void) { onUnauthorized = f; }

export async function api<T = any>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch('/api' + path, {
    method,
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', 'X-Halyard': '1' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let data: any = null;
  try { data = await res.json(); } catch { /* empty */ }
  if (!res.ok) {
    if (res.status === 401 && !path.startsWith('/login') && !path.startsWith('/setup')) onUnauthorized();
    throw new ApiError(res.status, data?.error || res.statusText, data);
  }
  return data as T;
}

export const get = <T = any>(p: string) => api<T>('GET', p);
export const post = <T = any>(p: string, b?: unknown) => api<T>('POST', p, b ?? {});
export const put = <T = any>(p: string, b: unknown) => api<T>('PUT', p, b);
export const del = <T = any>(p: string) => api<T>('DELETE', p);

export function ago(iso: string): string {
  const t = new Date(iso).getTime();
  if (!t) return '';
  const s = Math.max(0, (Date.now() - t) / 1000);
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
}

export function fmtDate(iso: string): string {
  const d = new Date(iso);
  if (!d.getTime() || d.getFullYear() < 2000) return '—';
  return d.toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' });
}

export function fmtTime(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
}

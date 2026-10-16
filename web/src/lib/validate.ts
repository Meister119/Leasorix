// Client side validation. The backend validates everything again; these
// checks give immediate feedback in forms.

export function parseIPv4(s: string): number | null {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(s.trim());
  if (!m) return null;
  let v = 0;
  for (let i = 1; i <= 4; i++) {
    const part = m[i];
    if (part.length > 1 && part.startsWith("0")) return null;
    const n = Number(part);
    if (n > 255) return null;
    v = v * 256 + n;
  }
  return v;
}

export function formatIPv4(v: number): string {
  return [v >>> 24, (v >>> 16) & 255, (v >>> 8) & 255, v & 255].join(".");
}

export interface Prefix {
  network: number;
  bits: number;
}

export function parsePrefix(s: string): Prefix | null {
  const [addr, len, ...rest] = s.trim().split("/");
  if (rest.length > 0 || len === undefined || !/^\d{1,2}$/.test(len)) return null;
  const ip = parseIPv4(addr);
  const bits = Number(len);
  if (ip === null || bits < 0 || bits > 32) return null;
  return { network: ip, bits };
}

export function mask(bits: number): number {
  return bits === 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0;
}

export function networkOf(p: Prefix): number {
  return (p.network & mask(p.bits)) >>> 0;
}

export function broadcastOf(p: Prefix): number {
  return (networkOf(p) | (~mask(p.bits) >>> 0)) >>> 0;
}

export function inSubnet(p: Prefix, ip: number): boolean {
  return (ip & mask(p.bits)) >>> 0 === networkOf(p);
}

export function overlaps(a: Prefix, b: Prefix): boolean {
  return networkOf(a) <= broadcastOf(b) && networkOf(b) <= broadcastOf(a);
}

export type Errors = Record<string, string>;

export interface ScopeForm {
  name: string;
  subnet: string;
  start: string;
  end: string;
  lease_time: number;
}

/** Validates a scope like the backend does (prefix /8../30, range inside
 * the subnet, no overlap with other scopes). */
export function validateScope(f: ScopeForm, others: { name: string; subnet: string }[] = []): Errors {
  const e: Errors = {};
  if (!f.name.trim()) e.name = "Name ist erforderlich";
  const p = parsePrefix(f.subnet);
  if (!p) {
    e.subnet = "Subnetz im Format 192.168.1.0/24 angeben";
  } else if (p.bits < 8 || p.bits > 30) {
    e.subnet = "Präfixlänge muss zwischen /8 und /30 liegen";
  } else if (networkOf(p) !== p.network) {
    e.subnet = `Host-Bits gesetzt – Netzadresse ist ${formatIPv4(networkOf(p))}/${p.bits}`;
  } else {
    const clash = others.find((o) => {
      const op = parsePrefix(o.subnet);
      return op !== null && overlaps(p, op);
    });
    if (clash) e.subnet = `Überschneidet sich mit Scope „${clash.name}“ (${clash.subnet})`;
  }
  const start = parseIPv4(f.start);
  const end = parseIPv4(f.end);
  if (start === null) e.start = "Keine gültige IPv4-Adresse";
  if (end === null) e.end = "Keine gültige IPv4-Adresse";
  if (p && start !== null && !e.subnet) {
    if (!inSubnet(p, start)) e.start = "Liegt nicht im Subnetz";
    else if (start === networkOf(p) || start === broadcastOf(p)) e.start = "Netz- oder Broadcast-Adresse";
  }
  if (p && end !== null && !e.subnet) {
    if (!inSubnet(p, end)) e.end = "Liegt nicht im Subnetz";
    else if (end === networkOf(p) || end === broadcastOf(p)) e.end = "Netz- oder Broadcast-Adresse";
  }
  if (start !== null && end !== null && !e.start && !e.end && start > end) e.end = "Ende liegt vor dem Anfang";
  if (!Number.isInteger(f.lease_time) || (f.lease_time !== 0 && f.lease_time < 10)) {
    e.lease_time = "0 (unbegrenzt) oder mindestens 10 Sekunden";
  }
  return e;
}

/** Validates an address range that must lie inside [min, max]. */
export function validateRange(start: string, end: string, within?: { start: string; end: string }): Errors {
  const e: Errors = {};
  const s = parseIPv4(start);
  const en = parseIPv4(end);
  if (s === null) e.start = "Keine gültige IPv4-Adresse";
  if (en === null) e.end = "Keine gültige IPv4-Adresse";
  if (s !== null && en !== null && s > en) e.end = "Ende liegt vor dem Anfang";
  if (within && s !== null && en !== null && !e.end) {
    const lo = parseIPv4(within.start)!;
    const hi = parseIPv4(within.end)!;
    if (s < lo || en > hi) e.start = `Muss im Scope-Bereich ${within.start} – ${within.end} liegen`;
  }
  return e;
}

export function normalizeMAC(s: string): string | null {
  const clean = s.trim().replace(/[:\-.\s]/g, "").toLowerCase();
  if (!/^[0-9a-f]{12}$/.test(clean)) return null;
  return clean.match(/../g)!.join(":");
}

export function validMACPattern(s: string): boolean {
  const v = s.trim().toLowerCase().replace(/-/g, ":");
  if (!/[*?]/.test(v)) return normalizeMAC(v) !== null;
  return /^[0-9a-f:*?]+$/.test(v);
}

export function validateReservation(
  r: { ip: string; mac: string; client_id: string },
  subnet?: string,
): Errors {
  const e: Errors = {};
  const ip = parseIPv4(r.ip);
  if (ip === null) e.ip = "Keine gültige IPv4-Adresse";
  else if (subnet) {
    const p = parsePrefix(subnet);
    if (p && (!inSubnet(p, ip) || ip === networkOf(p) || ip === broadcastOf(p))) e.ip = `Keine nutzbare Adresse in ${subnet}`;
  }
  if (!r.mac.trim() && !r.client_id.trim()) e.mac = "MAC-Adresse oder Client-ID angeben";
  if (r.mac.trim() && normalizeMAC(r.mac) === null) e.mac = "Ungültige MAC-Adresse";
  if (r.client_id.trim() && !/^([0-9a-fA-F]{2}[:-]?)+$/.test(r.client_id.trim())) e.client_id = "Client-ID als Hex angeben";
  return e;
}

export const hasErrors = (e: Errors) => Object.keys(e).length > 0;

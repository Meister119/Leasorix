import { locale, messages } from "../i18n";

const m = messages({
  en: {
    unlimited: "unlimited",
    in: "in {t}",
    ago: "{t} ago",
    active: "active",
    expired: "expired",
    released: "released",
    declined: "declined",
    admin: "Administrator",
    operator: "Operator",
    readonly: "Read-only",
    NORMAL: "Normal",
    COMMUNICATION_INTERRUPTED: "Communication interrupted",
    PARTNER_DOWN: "Partner down",
    RECOVER: "Recovering",
    RECOVER_DONE: "Recovery done",
  },
  de: {
    unlimited: "unbegrenzt",
    in: "in {t}",
    ago: "vor {t}",
    active: "aktiv",
    expired: "abgelaufen",
    released: "freigegeben",
    declined: "abgelehnt",
    admin: "Administrator",
    operator: "Operator",
    readonly: "Nur lesen",
    NORMAL: "Normal",
    COMMUNICATION_INTERRUPTED: "Verbindung unterbrochen",
    PARTNER_DOWN: "Partner ausgefallen",
    RECOVER: "Abgleich",
    RECOVER_DONE: "Abgleich fertig",
  },
});

export function formatDateTime(s?: string | null): string {
  if (!s) return "–";
  const d = new Date(s);
  if (d.getUTCFullYear() >= 9999) return m("unlimited");
  return new Intl.DateTimeFormat(locale(), { dateStyle: "short", timeStyle: "medium" }).format(d);
}

export function formatDuration(seconds: number): string {
  if (seconds === 0) return m("unlimited");
  const units: [number, string][] = [
    [86400, "d"],
    [3600, "h"],
    [60, "min"],
    [1, "s"],
  ];
  const parts: string[] = [];
  let rest = seconds;
  for (const [n, u] of units) {
    if (rest >= n) {
      parts.push(`${Math.floor(rest / n)} ${u}`);
      rest %= n;
    }
  }
  return parts.slice(0, 2).join(" ");
}

export function relativeTime(s: string): string {
  const diff = (new Date(s).getTime() - Date.now()) / 1000;
  if (new Date(s).getUTCFullYear() >= 9999) return m("unlimited");
  const abs = Math.abs(diff);
  const text = abs < 60 ? `${Math.round(abs)} s` : abs < 3600 ? `${Math.round(abs / 60)} min` : abs < 86400 ? `${Math.round(abs / 3600)} h` : `${Math.round(abs / 86400)} d`;
  return m(diff >= 0 ? "in" : "ago", { t: text });
}

/** Label of a lease state (active, expired, released, declined). */
export function stateLabel(state: string): string {
  return state === "active" || state === "expired" || state === "released" || state === "declined" ? m(state) : state;
}

/** The lease states with their labels, in display order. */
export function stateLabels(): [string, string][] {
  return (["active", "expired", "released", "declined"] as const).map((s) => [s, m(s)]);
}

/** Label of a user role. */
export function roleLabel(role: string): string {
  return role === "admin" || role === "operator" || role === "readonly" ? m(role) : role;
}

/** Label of an HA state. */
export function haStateLabel(state: string): string {
  switch (state) {
    case "NORMAL":
    case "COMMUNICATION_INTERRUPTED":
    case "PARTNER_DOWN":
    case "RECOVER":
    case "RECOVER_DONE":
      return m(state);
  }
  return state;
}

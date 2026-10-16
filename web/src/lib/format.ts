const dtf = new Intl.DateTimeFormat("de-DE", { dateStyle: "short", timeStyle: "medium" });

export function formatDateTime(s?: string | null): string {
  if (!s) return "–";
  const d = new Date(s);
  if (d.getUTCFullYear() >= 9999) return "unbegrenzt";
  return dtf.format(d);
}

export function formatDuration(seconds: number): string {
  if (seconds === 0) return "unbegrenzt";
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
  if (new Date(s).getUTCFullYear() >= 9999) return "unbegrenzt";
  const abs = Math.abs(diff);
  const text = abs < 60 ? `${Math.round(abs)} s` : abs < 3600 ? `${Math.round(abs / 60)} min` : abs < 86400 ? `${Math.round(abs / 3600)} h` : `${Math.round(abs / 86400)} d`;
  return diff >= 0 ? `in ${text}` : `vor ${text}`;
}

export const stateLabel: Record<string, string> = {
  active: "aktiv",
  expired: "abgelaufen",
  released: "freigegeben",
  declined: "abgelehnt",
};

export const roleLabel: Record<string, string> = {
  admin: "Administrator",
  operator: "Operator",
  readonly: "Nur lesen",
};

export const haStateLabel: Record<string, string> = {
  NORMAL: "Normal",
  COMMUNICATION_INTERRUPTED: "Verbindung unterbrochen",
  PARTNER_DOWN: "Partner ausgefallen",
  RECOVER: "Abgleich",
  RECOVER_DONE: "Abgleich fertig",
};

import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { api, unwrap } from "../api/client";
import { Badge, Card, PageHeader, Progress, Spinner, Table, Td } from "../components/ui";
import { messages } from "../i18n";
import { formatDateTime, haStateLabel } from "../lib/format";

const m = messages({
  en: {
    subtitle: "Utilization, packets and events at a glance",
    activeLeases: "Active leases",
    freeAddresses: "Free addresses",
    ofUsable: "of {n} usable",
    pps: "Packets/s",
    received: "{n} received in total",
    ha: "High availability",
    off: "off",
    partner: "Partner {id}: {status}",
    connected: "connected",
    unreachable: "unreachable",
    single: "Single server",
    utilization: "Utilization per scope",
    scope: "Scope",
    inUse: "In use",
    utilizationCol: "Utilization",
    recent: "Recent events",
    showAll: "Show all",
    noEvents: "No events yet.",
    dropped: "Unanswered packets",
  },
  de: {
    subtitle: "Übersicht über Auslastung, Pakete und Ereignisse",
    activeLeases: "Aktive Leases",
    freeAddresses: "Freie Adressen",
    ofUsable: "von {n} nutzbaren",
    pps: "Pakete/s",
    received: "{n} empfangen gesamt",
    ha: "Hochverfügbarkeit",
    off: "aus",
    partner: "Partner {id}: {status}",
    connected: "verbunden",
    unreachable: "nicht erreichbar",
    single: "Einzelinstanz",
    utilization: "Auslastung je Scope",
    scope: "Scope",
    inUse: "Belegt",
    utilizationCol: "Auslastung",
    recent: "Letzte Ereignisse",
    showAll: "Alle anzeigen",
    noEvents: "Noch keine Ereignisse.",
    dropped: "Unbeantwortete Pakete",
  },
});

function sum(m: Record<string, number> | undefined) {
  return Object.values(m ?? {}).reduce((a, b) => a + b, 0);
}

function Stat({ label, value, hint }: { label: string; value: string | number; hint?: string }) {
  return (
    <div className="rounded-lg border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
      <div className="text-xs font-medium tracking-wide text-slate-500 uppercase dark:text-slate-400">{label}</div>
      <div className="mt-1 text-2xl font-semibold tabular-nums">{value}</div>
      {hint && <div className="mt-1 text-xs text-slate-500 dark:text-slate-400">{hint}</div>}
    </div>
  );
}

export function DashboardPage() {
  const stats = useQuery({ queryKey: ["stats"], queryFn: () => unwrap(api.GET("/stats")), refetchInterval: 5000 });
  const ha = useQuery({ queryKey: ["ha"], queryFn: () => unwrap(api.GET("/ha/status")), refetchInterval: 10000 });
  const events = useQuery({
    queryKey: ["audit", "recent"],
    queryFn: () => unwrap(api.GET("/audit", { params: { query: { limit: 10 } } })),
    refetchInterval: 10000,
  });

  // Packets per second from successive counter samples.
  const prev = useRef<{ t: number; rx: number } | null>(null);
  const [pps, setPps] = useState<number | null>(null);
  useEffect(() => {
    if (!stats.data) return;
    const now = Date.now();
    const rx = sum(stats.data.packets_received);
    if (prev.current && now > prev.current.t) {
      setPps(Math.max(0, (rx - prev.current.rx) / ((now - prev.current.t) / 1000)));
    }
    prev.current = { t: now, rx };
  }, [stats.data]);

  if (stats.isLoading) return <Spinner />;
  const s = stats.data;
  const usable = s?.scopes.reduce((a, b) => a + b.usable, 0) ?? 0;
  const available = s?.scopes.reduce((a, b) => a + b.available, 0) ?? 0;

  return (
    <>
      <PageHeader title="Dashboard" subtitle={m("subtitle")} />
      <div className="mb-6 grid grid-cols-2 gap-4 lg:grid-cols-4">
        <Stat label={m("activeLeases")} value={s?.active_leases ?? 0} />
        <Stat label={m("freeAddresses")} value={available} hint={m("ofUsable", { n: usable })} />
        <Stat label={m("pps")} value={pps === null ? "–" : pps.toFixed(1)} hint={m("received", { n: sum(s?.packets_received) })} />
        <Stat
          label={m("ha")}
          value={ha.data?.enabled ? haStateLabel(ha.data.state ?? "") : m("off")}
          hint={ha.data?.enabled ? m("partner", { id: ha.data.peer_id ?? "–", status: m(ha.data.connected ? "connected" : "unreachable") }) : m("single")}
        />
      </div>
      <div className="grid gap-6 lg:grid-cols-2">
        <Card title={m("utilization")}>
          <Table head={[m("scope"), m("inUse"), m("utilizationCol")]} empty={s?.scopes.length === 0}>
            {s?.scopes.map((sc) => (
              <tr key={sc.scope_id}>
                <Td>
                  <Link className="font-medium text-teal-700 hover:underline dark:text-teal-400" to={`/scopes/${sc.scope_id}`}>
                    {sc.name}
                  </Link>
                  <div className="font-mono text-xs text-slate-500 dark:text-slate-400">{sc.subnet}</div>
                </Td>
                <Td className="tabular-nums">
                  {sc.usable - sc.available} / {sc.usable}
                </Td>
                <Td className="w-full min-w-32">
                  <Progress percent={sc.utilization_percent} />
                </Td>
              </tr>
            ))}
          </Table>
        </Card>
        <Card title={m("recent")} actions={<Link to="/audit" className="text-xs text-teal-700 hover:underline dark:text-teal-400">{m("showAll")}</Link>}>
          <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
            {events.data?.items.map((e) => (
              <li key={e.id} className="flex flex-wrap items-center justify-between gap-2 py-2">
                <span>
                  <Badge color={e.action.startsWith("lease.") ? "blue" : "teal"}>{e.action}</Badge>{" "}
                  <span className="font-mono text-xs">{e.object_id}</span>
                </span>
                <span className="text-xs text-slate-500 dark:text-slate-400">
                  {e.actor} · {formatDateTime(e.time)}
                </span>
              </li>
            ))}
            {events.data?.items.length === 0 && <li className="py-4 text-center text-slate-500">{m("noEvents")}</li>}
          </ul>
        </Card>
      </div>
      {s && Object.keys(s.packets_dropped).length > 0 && (
        <Card title={m("dropped")} className="mt-6">
          <div className="flex flex-wrap gap-2">
            {Object.entries(s.packets_dropped).map(([k, v]) => (
              <Badge key={k} color="amber">
                {k}: {v}
              </Badge>
            ))}
          </div>
        </Card>
      )}
    </>
  );
}

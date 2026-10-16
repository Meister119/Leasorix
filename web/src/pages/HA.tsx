import { useQuery } from "@tanstack/react-query";
import { api, unwrap } from "../api/client";
import { Alert, Badge, Card, PageHeader, Spinner } from "../components/ui";
import { formatDateTime } from "../lib/format";

export function HAPage() {
  const ha = useQuery({ queryKey: ["ha"], queryFn: () => unwrap(api.GET("/ha/status")), refetchInterval: 5000 });
  if (ha.isLoading) return <Spinner />;
  const s = ha.data;
  return (
    <>
      <PageHeader title="Hochverfügbarkeit" subtitle="Cluster-Status und Failover" />
      {!s?.enabled ? (
        <Alert kind="info">
          Diese Instanz läuft ohne Partner. Hochverfügbarkeit wird in der Konfigurationsdatei (Abschnitt <code>ha</code>) aktiviert.
        </Alert>
      ) : (
        <Card title="Partnerschaft">
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
            <dt className="text-slate-500">Modus</dt>
            <dd>{s.mode}</dd>
            <dt className="text-slate-500">Rolle</dt>
            <dd>{s.role}</dd>
            <dt className="text-slate-500">Zustand</dt>
            <dd>
              <Badge color={s.state === "normal" ? "green" : "amber"}>{s.state}</Badge>
            </dd>
            <dt className="text-slate-500">Partner</dt>
            <dd className="font-mono">{s.peer}</dd>
            <dt className="text-slate-500">Partner-Zustand</dt>
            <dd>{s.peer_state}</dd>
            <dt className="text-slate-500">Konfigurations-Leader</dt>
            <dd>{s.config_leader ? "ja" : "nein (Konfiguration nur lesbar)"}</dd>
            <dt className="text-slate-500">Letzter Kontakt</dt>
            <dd>{formatDateTime(s.last_contact)}</dd>
          </dl>
        </Card>
      )}
    </>
  );
}

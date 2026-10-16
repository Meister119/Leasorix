import { useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { Alert, Badge, Button, Card, Input, PageHeader, Spinner, useFeedback } from "../components/ui";
import { downloadFile, readJSONFile } from "../lib/download";
import { formatDateTime, formatDuration, haStateLabel, relativeTime } from "../lib/format";
import { LeaseExportButton } from "./Leases";

const stateColor: Record<string, "green" | "amber" | "red" | "blue" | "slate"> = {
  NORMAL: "green",
  COMMUNICATION_INTERRUPTED: "amber",
  PARTNER_DOWN: "red",
  RECOVER: "blue",
  RECOVER_DONE: "blue",
};

const stateHelp: Record<string, string> = {
  NORMAL: "Beide Server arbeiten zusammen; jede Lease-Änderung wird zum Partner repliziert.",
  COMMUNICATION_INTERRUPTED:
    "Der Partner ist nicht erreichbar. Diese Instanz vergibt nur Adressen aus ihrem eigenen Pool-Anteil und verlängert bestehende Leases höchstens um die MCLT.",
  PARTNER_DOWN: "Der Partner gilt als ausgefallen. Nach Ablauf der MCLT vergibt diese Instanz Adressen aus dem gesamten Pool.",
  RECOVER: "Abgleich der Lease-Datenbank mit dem Partner. Bis er abgeschlossen ist, beantwortet diese Instanz keine DHCP-Anfragen.",
  RECOVER_DONE: "Eigener Abgleich abgeschlossen, wartet auf den Partner.",
};

function StateBadge({ state }: { state?: string }) {
  if (!state) return <span>–</span>;
  return <Badge color={stateColor[state] ?? "slate"}>{haStateLabel[state] ?? state}</Badge>;
}

function Stat({ label, children, hint }: { label: string; children: ReactNode; hint?: ReactNode }) {
  return (
    <div className="rounded-lg border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
      <div className="text-xs font-medium tracking-wide text-slate-500 uppercase dark:text-slate-400">{label}</div>
      <div className="mt-1 text-lg font-semibold">{children}</div>
      {hint && <div className="mt-1 text-xs text-slate-500 dark:text-slate-400">{hint}</div>}
    </div>
  );
}

export function HAPage() {
  const ha = useQuery({ queryKey: ["ha"], queryFn: () => unwrap(api.GET("/ha/status")), refetchInterval: 3000 });
  if (ha.isLoading) return <Spinner />;
  const s = ha.data;
  return (
    <>
      <PageHeader title="Hochverfügbarkeit" subtitle="Failover-Partnerschaft, Zertifikate und Notfall-Werkzeuge" />
      <div className="space-y-6">
        {s?.enabled ? <Relationship s={s} /> : <NotConfigured />}
        <div className="grid gap-6 lg:grid-cols-2">
          <CertificateGenerator nodeId={s?.node_id} peerId={s?.peer_id} />
          <EmergencyTools />
        </div>
      </div>
    </>
  );
}

function NotConfigured() {
  return (
    <Card title="Keine Partnerschaft konfiguriert">
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        Diese Instanz läuft ohne Partner. Eine HA-Partnerschaft wird auf beiden Servern in der Konfigurationsdatei (Abschnitt <code>ha</code>)
        eingerichtet, zum Beispiel:
      </p>
      <pre className="overflow-x-auto rounded-md bg-slate-100 p-3 text-xs dark:bg-slate-800">{`ha:
  mode: load-balance        # oder hot-standby
  node_id: dhcp-a
  role: primary             # Partner: secondary
  listen: ":8068"
  peer: { id: dhcp-b, address: "10.0.0.2:8068" }
  tls:
    mode: mtls              # Zertifikate unten erzeugen
    ca_file: /data/ha/ca.pem
    cert_file: /data/ha/dhcp-a.pem
    key_file: /data/ha/dhcp-a-key.pem`}</pre>
      <p className="mt-3 text-sm text-slate-600 dark:text-slate-400">
        Ohne Replikation lässt sich ein Scope alternativ klassisch aufteilen: <em>Scope öffnen → Split-Scope</em>.
      </p>
    </Card>
  );
}

function Relationship({ s }: { s: S["HAStatus"] }) {
  const { isAdmin } = useAuth();
  const qc = useQueryClient();
  const { toast, confirm } = useFeedback();
  const down = useMutation({
    mutationFn: () => unwrap(api.POST("/ha/partner-down")),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ha"] });
      toast("PARTNER_DOWN aktiviert");
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const mode =
    s.mode === "load-balance"
      ? `Load Balance ${s.load_balance_percent}/${100 - (s.load_balance_percent ?? 0)}`
      : `Hot Standby (Reserve ${s.standby_reserve_percent} %)`;
  const certSoon = s.cert_expires && new Date(s.cert_expires).getTime() - Date.now() < 30 * 86400 * 1000;
  const canDeclare = (s.state === "COMMUNICATION_INTERRUPTED" || s.state === "RECOVER") && !s.connected;
  return (
    <>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4" data-testid="ha-status">
        <Stat label="Zustand" hint={s.state_since ? `seit ${formatDateTime(s.state_since)}` : undefined}>
          <StateBadge state={s.state} />
        </Stat>
        <Stat label={`Partner ${s.peer_id ?? ""}`} hint={s.last_contact ? `letzter Kontakt ${relativeTime(s.last_contact)}` : "noch kein Kontakt"}>
          {s.connected ? <Badge color="green">verbunden</Badge> : <Badge color="red">nicht erreichbar</Badge>}{" "}
          {s.peer_state && (
            <span className="text-sm font-normal text-slate-500">
              ({s.connected ? "" : "zuletzt: "}
              {haStateLabel[s.peer_state] ?? s.peer_state})
            </span>
          )}
        </Stat>
        <Stat label="Modus" hint={`${s.role === "primary" ? "Primary" : "Secondary"}, Replikation ${s.replication}`}>
          {mode}
        </Stat>
        <Stat
          label="Konfiguration"
          hint={`Version ${s.config_version ?? "–"}${s.connected ? (s.config_in_sync ? ", synchron" : ", wird abgeglichen") : ""}`}
        >
          {s.config_leader ? "Leader" : "schreibgeschützt"}
        </Stat>
      </div>

      {s.state && <Alert kind={s.state === "NORMAL" ? "success" : "info"}>{stateHelp[s.state]}</Alert>}
      {!s.serving && <Alert>Diese Instanz beantwortet derzeit keine DHCP-Anfragen (Abgleich mit dem Partner ausstehend).</Alert>}
      {s.state === "PARTNER_DOWN" && s.takeover_at && (
        <Alert kind="info">
          Übernahme des gesamten Pools {new Date(s.takeover_at).getTime() > Date.now() ? relativeTime(s.takeover_at) : "aktiv"} (
          {formatDateTime(s.takeover_at)}).
        </Alert>
      )}
      {s.error && <Alert>Verbindung: {s.error}</Alert>}
      {s.resync_error && <Alert>Abgleich: {s.resync_error}</Alert>}
      {s.config_error && <Alert>Konfiguration: {s.config_error}</Alert>}
      {certSoon && <Alert>Das Knotenzertifikat läuft am {formatDateTime(s.cert_expires)} ab – bitte neue Zertifikate erzeugen und verteilen.</Alert>}

      <div className="grid gap-6 lg:grid-cols-2">
        <Card title="Details">
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
            <dt className="text-slate-500">Diese Instanz</dt>
            <dd className="font-mono">{s.node_id}</dd>
            <dt className="text-slate-500">Partner-Adresse</dt>
            <dd className="font-mono">{s.peer_address}</dd>
            <dt className="text-slate-500">Partner-Version</dt>
            <dd>{s.peer_version || "–"}</dd>
            <dt className="text-slate-500">MCLT</dt>
            <dd>{formatDuration(s.mclt ?? 0)}</dd>
            <dt className="text-slate-500">Auto-Partner-Down</dt>
            <dd>{s.auto_partner_down ? `nach ${formatDuration(s.auto_partner_down)}` : "nur manuell"}</dd>
            <dt className="text-slate-500">Absicherung</dt>
            <dd>{s.tls_mode === "mtls" ? "mTLS" : "TLS + Shared Secret"}</dd>
            {s.cert_expires && (
              <>
                <dt className="text-slate-500">Zertifikat gültig bis</dt>
                <dd>{formatDateTime(s.cert_expires)}</dd>
              </>
            )}
            <dt className="text-slate-500">Letzter Lease-Abgleich</dt>
            <dd>{formatDateTime(s.last_resync)}</dd>
          </dl>
        </Card>
        <Card title="Partner-Ausfall">
          <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
            Ist der Partner sicher ausgefallen (nicht nur die Verbindung getrennt), kann diese Instanz den gesamten Pool übernehmen. Nach Ablauf der
            MCLT ({formatDuration(s.mclt ?? 0)}) vergibt sie auch Adressen aus dem Anteil des Partners.
          </p>
          {isAdmin ? (
            <Button
              variant="danger"
              disabled={!canDeclare || down.isPending}
              onClick={async () =>
                (await confirm(
                  `Partner ${s.peer_id} als ausgefallen bestätigen? Läuft der Partner in Wirklichkeit noch (z. B. nur Netzwerktrennung), können doppelte Adressen entstehen.`,
                  { danger: true, confirmLabel: "Partner ist ausgefallen" },
                )) && down.mutate()
              }
            >
              Partner-Ausfall bestätigen
            </Button>
          ) : (
            <p className="text-xs text-slate-500">Nur Administratoren können den Partner-Ausfall bestätigen.</p>
          )}
          {!canDeclare && s.state !== "PARTNER_DOWN" && <p className="mt-2 text-xs text-slate-500">Nur möglich, solange der Partner nicht erreichbar ist.</p>}
        </Card>
      </div>
    </>
  );
}

function CertificateGenerator({ nodeId, peerId }: { nodeId?: string; peerId?: string }) {
  const { isAdmin } = useAuth();
  const [nodes, setNodes] = useState(nodeId && peerId ? `${nodeId}, ${peerId}` : "dhcp-a, dhcp-b");
  const [certs, setCerts] = useState<S["HACertificates"] | null>(null);
  const [err, setErr] = useState("");
  const gen = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/ha/certificates", {
          body: {
            nodes: nodes
              .split(/[\s,]+/)
              .map((n) => n.trim())
              .filter(Boolean),
          },
        }),
      ),
    onSuccess: (c) => {
      setCerts(c);
      setErr("");
    },
    onError: (e) => setErr(errorMessage(e)),
  });
  return (
    <Card title="mTLS-Zertifikate erzeugen">
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        Erzeugt eine neue CA und je Knoten ein Zertifikat (Name = Node-ID). Nichts davon wird auf dem Server gespeichert; der CA-Schlüssel wird
        verworfen. Alternativ: <code>leasorixd ha certs --out DIR dhcp-a dhcp-b</code>.
      </p>
      {isAdmin ? (
        <form
          className="flex flex-wrap items-end gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            gen.mutate();
          }}
        >
          <div className="min-w-56 flex-1">
            <Input label="Node-IDs (Komma-getrennt)" value={nodes} onChange={(e) => setNodes(e.target.value)} />
          </div>
          <Button type="submit" variant="primary" disabled={gen.isPending}>
            Erzeugen
          </Button>
        </form>
      ) : (
        <p className="text-xs text-slate-500">Nur Administratoren können Zertifikate erzeugen.</p>
      )}
      {err && (
        <div className="mt-3">
          <Alert>{err}</Alert>
        </div>
      )}
      {certs && (
        <div className="mt-4 space-y-2">
          <Alert kind="success">
            Gültig bis {formatDateTime(certs.not_after)}. Jeder Knoten braucht <code>ca.pem</code> sowie sein eigenes Zertifikat und seinen Schlüssel.
            Schlüssel nur über sichere Wege übertragen.
          </Alert>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" onClick={() => downloadFile("ca.pem", certs.ca_cert, "application/x-pem-file")}>
              ca.pem
            </Button>
            {certs.nodes.map((n) => (
              <span key={n.node_id} className="flex gap-2">
                <Button size="sm" onClick={() => downloadFile(`${n.node_id}.pem`, n.cert, "application/x-pem-file")}>
                  {n.node_id}.pem
                </Button>
                <Button size="sm" onClick={() => downloadFile(`${n.node_id}-key.pem`, n.key, "application/x-pem-file")}>
                  {n.node_id}-key.pem
                </Button>
              </span>
            ))}
          </div>
        </div>
      )}
    </Card>
  );
}

function EmergencyTools() {
  const { isAdmin } = useAuth();
  const qc = useQueryClient();
  const [result, setResult] = useState<S["LeaseImportResult"] | null>(null);
  const [err, setErr] = useState("");
  const imp = useMutation({
    mutationFn: async (f: File) => unwrap(api.POST("/leases/import", { body: await readJSONFile<S["LeaseExport"]>(f) })),
    onSuccess: (r) => {
      setResult(r);
      setErr("");
      qc.invalidateQueries({ queryKey: ["leases"] });
      qc.invalidateQueries({ queryKey: ["stats"] });
    },
    onError: (e) => {
      setResult(null);
      setErr(errorMessage(e));
    },
  });
  return (
    <Card title="Notfall: Lease-Datenbank sichern und einspielen">
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        Der Export enthält alle Lease-Datensätze. Beim Import werden sie mit dem Bestand zusammengeführt (neuere Änderungen gewinnen, Konflikte
        werden wie beim HA-Abgleich aufgelöst). Offline per CLI: <code>leasorixd leases export|import</code>.
      </p>
      <div className="flex flex-wrap items-center gap-3">
        <LeaseExportButton />
        {isAdmin && (
          <label className="inline-flex cursor-pointer items-center rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-50 dark:border-slate-700 dark:hover:bg-slate-800">
            Import…
            <input
              type="file"
              accept="application/json,.json"
              className="sr-only"
              data-testid="lease-import"
              onChange={(e) => {
                const f = e.target.files?.[0];
                if (f) imp.mutate(f);
                e.target.value = "";
              }}
            />
          </label>
        )}
      </div>
      {err && (
        <div className="mt-3">
          <Alert>{err}</Alert>
        </div>
      )}
      {result && (
        <div className="mt-3">
          <Alert kind="success">
            {result.records} Datensätze gelesen, {result.changed} übernommen, {result.conflicts} Konflikte aufgelöst.
          </Alert>
        </div>
      )}
    </Card>
  );
}

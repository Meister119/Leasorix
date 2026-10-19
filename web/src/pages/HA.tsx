import { useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { Alert, Badge, Button, Card, Input, PageHeader, Spinner, useFeedback } from "../components/ui";
import { messages } from "../i18n";
import { c } from "../i18n/common";
import { downloadFile, readJSONFile } from "../lib/download";
import { formatDateTime, formatDuration, haStateLabel, relativeTime } from "../lib/format";
import { LeaseExportButton } from "./Leases";

const m = messages({
  en: {
    title: "High availability",
    subtitle: "Failover partnership, certificates and emergency tools",
    helpNormal: "Both servers work together; every lease change is replicated to the partner.",
    helpInterrupted:
      "The partner is unreachable. This instance only hands out addresses from its own share of the pool and extends existing leases by at most the MCLT.",
    helpPartnerDown: "The partner is considered down. Once the MCLT has elapsed, this instance hands out addresses from the entire pool.",
    helpRecover: "Synchronizing the lease database with the partner. Until this is complete, this instance does not answer DHCP requests.",
    helpRecoverDone: "Own synchronization complete, waiting for the partner.",
    notConfigured: "No partnership configured",
    notConfiguredInfo:
      "This instance runs without a partner. An HA partnership is set up on both servers in the configuration file (section {section}), for example:",
    yamlMode: "or hot-standby",
    yamlRole: "partner: secondary",
    yamlTls: "generate the certificates below",
    splitHint: "Without replication, a scope can alternatively be split the classic way:",
    splitPath: "Open scope → Split scope",
    partnerDownActivated: "PARTNER_DOWN activated",
    loadBalance: "Load balance {own}/{peer}",
    hotStandby: "Hot standby ({n}% reserve)",
    state: "State",
    since: "since {t}",
    partner: "Partner {id}",
    lastContact: "last contact {t}",
    noContact: "no contact yet",
    connected: "connected",
    unreachable: "unreachable",
    lastState: "last: ",
    mode: "Mode",
    modeHint: "{role}, replication {replication}",
    configuration: "Configuration",
    configVersion: "Version {v}",
    inSync: "in sync",
    syncing: "syncing",
    leader: "Leader",
    readOnly: "read-only",
    notServing: "This instance is currently not answering DHCP requests (synchronization with the partner pending).",
    takeoverPending: "Takeover of the entire pool {when} ({time}).",
    takeoverActive: "Takeover of the entire pool active ({time}).",
    connectionError: "Connection: {error}",
    resyncError: "Synchronization: {error}",
    configError: "Configuration: {error}",
    certExpiring: "The node certificate expires on {time} – please generate and distribute new certificates.",
    thisInstance: "This instance",
    peerAddress: "Partner address",
    peerVersion: "Partner version",
    autoPartnerDown: "Auto partner down",
    after: "after {d}",
    manualOnly: "manual only",
    security: "Security",
    sharedSecret: "TLS + shared secret",
    certValidUntil: "Certificate valid until",
    lastResync: "Last lease sync",
    partnerFailure: "Partner failure",
    partnerFailureInfo:
      "If the partner has definitely failed (not just lost its connection), this instance can take over the entire pool. Once the MCLT ({mclt}) has elapsed, it also hands out addresses from the partner’s share.",
    declareConfirm:
      "Confirm that partner {id} has failed? If the partner is actually still running (e.g. only a network split), duplicate addresses can occur.",
    declareConfirmLabel: "Partner has failed",
    declare: "Confirm partner failure",
    declareAdminOnly: "Only administrators can confirm a partner failure.",
    declareUnreachableOnly: "Only possible while the partner is unreachable.",
    certTitle: "Generate mTLS certificates",
    certInfo:
      "Generates a new CA and one certificate per node (name = node ID). None of it is stored on the server; the CA key is discarded. Alternatively: {cmd}.",
    nodeIds: "Node IDs (comma-separated)",
    generate: "Generate",
    certAdminOnly: "Only administrators can generate certificates.",
    certsGenerated:
      "Valid until {time}. Each node needs {ca} as well as its own certificate and key. Transfer keys only over secure channels.",
    emergencyTitle: "Emergency: back up and restore the lease database",
    emergencyInfo:
      "The export contains all lease records. On import, they are merged with the existing data (newer changes win, conflicts are resolved as in an HA sync). Offline via CLI: {cmd}.",
    import: "Import…",
    importResult: "{records} records read, {changed} applied, {conflicts} conflicts resolved.",
  },
  de: {
    title: "Hochverfügbarkeit",
    subtitle: "Failover-Partnerschaft, Zertifikate und Notfall-Werkzeuge",
    helpNormal: "Beide Server arbeiten zusammen; jede Lease-Änderung wird zum Partner repliziert.",
    helpInterrupted:
      "Der Partner ist nicht erreichbar. Diese Instanz vergibt nur Adressen aus ihrem eigenen Pool-Anteil und verlängert bestehende Leases höchstens um die MCLT.",
    helpPartnerDown: "Der Partner gilt als ausgefallen. Nach Ablauf der MCLT vergibt diese Instanz Adressen aus dem gesamten Pool.",
    helpRecover: "Abgleich der Lease-Datenbank mit dem Partner. Bis er abgeschlossen ist, beantwortet diese Instanz keine DHCP-Anfragen.",
    helpRecoverDone: "Eigener Abgleich abgeschlossen, wartet auf den Partner.",
    notConfigured: "Keine Partnerschaft konfiguriert",
    notConfiguredInfo:
      "Diese Instanz läuft ohne Partner. Eine HA-Partnerschaft wird auf beiden Servern in der Konfigurationsdatei (Abschnitt {section}) eingerichtet, zum Beispiel:",
    yamlMode: "oder hot-standby",
    yamlRole: "Partner: secondary",
    yamlTls: "Zertifikate unten erzeugen",
    splitHint: "Ohne Replikation lässt sich ein Scope alternativ klassisch aufteilen:",
    splitPath: "Scope öffnen → Split-Scope",
    partnerDownActivated: "PARTNER_DOWN aktiviert",
    loadBalance: "Load Balance {own}/{peer}",
    hotStandby: "Hot Standby (Reserve {n} %)",
    state: "Zustand",
    since: "seit {t}",
    partner: "Partner {id}",
    lastContact: "letzter Kontakt {t}",
    noContact: "noch kein Kontakt",
    connected: "verbunden",
    unreachable: "nicht erreichbar",
    lastState: "zuletzt: ",
    mode: "Modus",
    modeHint: "{role}, Replikation {replication}",
    configuration: "Konfiguration",
    configVersion: "Version {v}",
    inSync: "synchron",
    syncing: "wird abgeglichen",
    leader: "Leader",
    readOnly: "schreibgeschützt",
    notServing: "Diese Instanz beantwortet derzeit keine DHCP-Anfragen (Abgleich mit dem Partner ausstehend).",
    takeoverPending: "Übernahme des gesamten Pools {when} ({time}).",
    takeoverActive: "Übernahme des gesamten Pools aktiv ({time}).",
    connectionError: "Verbindung: {error}",
    resyncError: "Abgleich: {error}",
    configError: "Konfiguration: {error}",
    certExpiring: "Das Knotenzertifikat läuft am {time} ab – bitte neue Zertifikate erzeugen und verteilen.",
    thisInstance: "Diese Instanz",
    peerAddress: "Partner-Adresse",
    peerVersion: "Partner-Version",
    autoPartnerDown: "Auto-Partner-Down",
    after: "nach {d}",
    manualOnly: "nur manuell",
    security: "Absicherung",
    sharedSecret: "TLS + Shared Secret",
    certValidUntil: "Zertifikat gültig bis",
    lastResync: "Letzter Lease-Abgleich",
    partnerFailure: "Partner-Ausfall",
    partnerFailureInfo:
      "Ist der Partner sicher ausgefallen (nicht nur die Verbindung getrennt), kann diese Instanz den gesamten Pool übernehmen. Nach Ablauf der MCLT ({mclt}) vergibt sie auch Adressen aus dem Anteil des Partners.",
    declareConfirm:
      "Partner {id} als ausgefallen bestätigen? Läuft der Partner in Wirklichkeit noch (z. B. nur Netzwerktrennung), können doppelte Adressen entstehen.",
    declareConfirmLabel: "Partner ist ausgefallen",
    declare: "Partner-Ausfall bestätigen",
    declareAdminOnly: "Nur Administratoren können den Partner-Ausfall bestätigen.",
    declareUnreachableOnly: "Nur möglich, solange der Partner nicht erreichbar ist.",
    certTitle: "mTLS-Zertifikate erzeugen",
    certInfo:
      "Erzeugt eine neue CA und je Knoten ein Zertifikat (Name = Node-ID). Nichts davon wird auf dem Server gespeichert; der CA-Schlüssel wird verworfen. Alternativ: {cmd}.",
    nodeIds: "Node-IDs (Komma-getrennt)",
    generate: "Erzeugen",
    certAdminOnly: "Nur Administratoren können Zertifikate erzeugen.",
    certsGenerated:
      "Gültig bis {time}. Jeder Knoten braucht {ca} sowie sein eigenes Zertifikat und seinen Schlüssel. Schlüssel nur über sichere Wege übertragen.",
    emergencyTitle: "Notfall: Lease-Datenbank sichern und einspielen",
    emergencyInfo:
      "Der Export enthält alle Lease-Datensätze. Beim Import werden sie mit dem Bestand zusammengeführt (neuere Änderungen gewinnen, Konflikte werden wie beim HA-Abgleich aufgelöst). Offline per CLI: {cmd}.",
    import: "Import…",
    importResult: "{records} Datensätze gelesen, {changed} übernommen, {conflicts} Konflikte aufgelöst.",
  },
});

const stateColor: Record<string, "green" | "amber" | "red" | "blue" | "slate"> = {
  NORMAL: "green",
  COMMUNICATION_INTERRUPTED: "amber",
  PARTNER_DOWN: "red",
  RECOVER: "blue",
  RECOVER_DONE: "blue",
};

const stateHelp: Record<string, Parameters<typeof m>[0]> = {
  NORMAL: "helpNormal",
  COMMUNICATION_INTERRUPTED: "helpInterrupted",
  PARTNER_DOWN: "helpPartnerDown",
  RECOVER: "helpRecover",
  RECOVER_DONE: "helpRecoverDone",
};

function StateBadge({ state }: { state?: string }) {
  if (!state) return <span>–</span>;
  return <Badge color={stateColor[state] ?? "slate"}>{haStateLabel(state)}</Badge>;
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
      <PageHeader title={m("title")} subtitle={m("subtitle")} />
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
  const [infoBefore, infoAfter] = m("notConfiguredInfo").split("{section}");
  return (
    <Card title={m("notConfigured")}>
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        {infoBefore}
        <code>ha</code>
        {infoAfter}
      </p>
      <pre className="overflow-x-auto rounded-md bg-slate-100 p-3 text-xs dark:bg-slate-800">{`ha:
  mode: load-balance        # ${m("yamlMode")}
  node_id: dhcp-a
  role: primary             # ${m("yamlRole")}
  listen: ":8068"
  peer: { id: dhcp-b, address: "10.0.0.2:8068" }
  tls:
    mode: mtls              # ${m("yamlTls")}
    ca_file: /data/ha/ca.pem
    cert_file: /data/ha/dhcp-a.pem
    key_file: /data/ha/dhcp-a-key.pem`}</pre>
      <p className="mt-3 text-sm text-slate-600 dark:text-slate-400">
        {m("splitHint")} <em>{m("splitPath")}</em>.
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
      toast(m("partnerDownActivated"));
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const mode =
    s.mode === "load-balance"
      ? m("loadBalance", { own: String(s.load_balance_percent), peer: 100 - (s.load_balance_percent ?? 0) })
      : m("hotStandby", { n: String(s.standby_reserve_percent) });
  const certSoon = s.cert_expires && new Date(s.cert_expires).getTime() - Date.now() < 30 * 86400 * 1000;
  const canDeclare = (s.state === "COMMUNICATION_INTERRUPTED" || s.state === "RECOVER") && !s.connected;
  return (
    <>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4" data-testid="ha-status">
        <Stat label={m("state")} hint={s.state_since ? m("since", { t: formatDateTime(s.state_since) }) : undefined}>
          <StateBadge state={s.state} />
        </Stat>
        <Stat label={m("partner", { id: s.peer_id ?? "" })} hint={s.last_contact ? m("lastContact", { t: relativeTime(s.last_contact) }) : m("noContact")}>
          {s.connected ? <Badge color="green">{m("connected")}</Badge> : <Badge color="red">{m("unreachable")}</Badge>}{" "}
          {s.peer_state && (
            <span className="text-sm font-normal text-slate-500">
              ({s.connected ? "" : m("lastState")}
              {haStateLabel(s.peer_state)})
            </span>
          )}
        </Stat>
        <Stat label={m("mode")} hint={m("modeHint", { role: s.role === "primary" ? "Primary" : "Secondary", replication: String(s.replication) })}>
          {mode}
        </Stat>
        <Stat
          label={m("configuration")}
          hint={`${m("configVersion", { v: s.config_version ?? "–" })}${s.connected ? `, ${s.config_in_sync ? m("inSync") : m("syncing")}` : ""}`}
        >
          {s.config_leader ? m("leader") : m("readOnly")}
        </Stat>
      </div>

      {s.state && <Alert kind={s.state === "NORMAL" ? "success" : "info"}>{stateHelp[s.state] && m(stateHelp[s.state])}</Alert>}
      {!s.serving && <Alert>{m("notServing")}</Alert>}
      {s.state === "PARTNER_DOWN" && s.takeover_at && (
        <Alert kind="info">
          {new Date(s.takeover_at).getTime() > Date.now()
            ? m("takeoverPending", { when: relativeTime(s.takeover_at), time: formatDateTime(s.takeover_at) })
            : m("takeoverActive", { time: formatDateTime(s.takeover_at) })}
        </Alert>
      )}
      {s.error && <Alert>{m("connectionError", { error: s.error })}</Alert>}
      {s.resync_error && <Alert>{m("resyncError", { error: s.resync_error })}</Alert>}
      {s.config_error && <Alert>{m("configError", { error: s.config_error })}</Alert>}
      {certSoon && <Alert>{m("certExpiring", { time: formatDateTime(s.cert_expires) })}</Alert>}

      <div className="grid gap-6 lg:grid-cols-2">
        <Card title={c("details")}>
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
            <dt className="text-slate-500">{m("thisInstance")}</dt>
            <dd className="font-mono">{s.node_id}</dd>
            <dt className="text-slate-500">{m("peerAddress")}</dt>
            <dd className="font-mono">{s.peer_address}</dd>
            <dt className="text-slate-500">{m("peerVersion")}</dt>
            <dd>{s.peer_version || "–"}</dd>
            <dt className="text-slate-500">MCLT</dt>
            <dd>{formatDuration(s.mclt ?? 0)}</dd>
            <dt className="text-slate-500">{m("autoPartnerDown")}</dt>
            <dd>{s.auto_partner_down ? m("after", { d: formatDuration(s.auto_partner_down) }) : m("manualOnly")}</dd>
            <dt className="text-slate-500">{m("security")}</dt>
            <dd>{s.tls_mode === "mtls" ? "mTLS" : m("sharedSecret")}</dd>
            {s.cert_expires && (
              <>
                <dt className="text-slate-500">{m("certValidUntil")}</dt>
                <dd>{formatDateTime(s.cert_expires)}</dd>
              </>
            )}
            <dt className="text-slate-500">{m("lastResync")}</dt>
            <dd>{formatDateTime(s.last_resync)}</dd>
          </dl>
        </Card>
        <Card title={m("partnerFailure")}>
          <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">{m("partnerFailureInfo", { mclt: formatDuration(s.mclt ?? 0) })}</p>
          {isAdmin ? (
            <Button
              variant="danger"
              disabled={!canDeclare || down.isPending}
              onClick={async () =>
                (await confirm(
                  m("declareConfirm", { id: String(s.peer_id) }),
                  { danger: true, confirmLabel: m("declareConfirmLabel") },
                )) && down.mutate()
              }
            >
              {m("declare")}
            </Button>
          ) : (
            <p className="text-xs text-slate-500">{m("declareAdminOnly")}</p>
          )}
          {!canDeclare && s.state !== "PARTNER_DOWN" && <p className="mt-2 text-xs text-slate-500">{m("declareUnreachableOnly")}</p>}
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
  const [infoBefore, infoAfter] = m("certInfo").split("{cmd}");
  const [doneBefore, doneAfter] = m("certsGenerated", { time: certs ? formatDateTime(certs.not_after) : "" }).split("{ca}");
  return (
    <Card title={m("certTitle")}>
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        {infoBefore}
        <code>leasorixd ha certs --out DIR dhcp-a dhcp-b</code>
        {infoAfter}
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
            <Input label={m("nodeIds")} value={nodes} onChange={(e) => setNodes(e.target.value)} />
          </div>
          <Button type="submit" variant="primary" disabled={gen.isPending}>
            {m("generate")}
          </Button>
        </form>
      ) : (
        <p className="text-xs text-slate-500">{m("certAdminOnly")}</p>
      )}
      {err && (
        <div className="mt-3">
          <Alert>{err}</Alert>
        </div>
      )}
      {certs && (
        <div className="mt-4 space-y-2">
          <Alert kind="success">
            {doneBefore}
            <code>ca.pem</code>
            {doneAfter}
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
  const [infoBefore, infoAfter] = m("emergencyInfo").split("{cmd}");
  return (
    <Card title={m("emergencyTitle")}>
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        {infoBefore}
        <code>leasorixd leases export|import</code>
        {infoAfter}
      </p>
      <div className="flex flex-wrap items-center gap-3">
        <LeaseExportButton />
        {isAdmin && (
          <label className="inline-flex cursor-pointer items-center rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium hover:bg-slate-50 dark:border-slate-700 dark:hover:bg-slate-800">
            {m("import")}
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
            {m("importResult", { records: result.records, changed: result.changed, conflicts: result.conflicts })}
          </Alert>
        </div>
      )}
    </Card>
  );
}

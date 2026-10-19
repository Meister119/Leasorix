import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "react-router";
import { api, ApiError, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { OptionsEditor } from "../components/OptionsEditor";
import { Alert, Badge, Button, Card, Input, Modal, PageHeader, Progress, Select, Spinner, Table, Tabs, Td, TextArea, Toggle, useFeedback } from "../components/ui";
import { messages } from "../i18n";
import { c } from "../i18n/common";
import { downloadFile } from "../lib/download";
import { formatDuration } from "../lib/format";
import { formatIPv4, parseIPv4 } from "../lib/validate";
import { hasErrors, validateRange, validateScope, type Errors } from "../lib/validate";
import { LeasesTable } from "./Leases";
import { PoliciesList } from "./Policies";
import { ReservationsTable } from "./Reservations";
import { leaseTimes, useScopes, useSuperscopes } from "./Scopes";
import { ScopeDNS } from "./ScopeDNS";

const m = messages({
  en: {
    split: "Split scope…",
    scopes: "Scopes",
    tabOverview: "Overview",
    tabExclusions: "Exclusions",
    tabOptions: "Options",
    tabReservations: "Reservations",
    tabPolicies: "Policies",
    tabDns: "DNS",
    tabLeases: "Leases",
    scopeOptions: "Scope options",
    overrideServer: "override server options",
    splitTitle: "Split scope – {name}",
    splitAction: "Split",
    splitInfo:
      "Splits the dynamic range between two independent servers without replication (classic fallback). This server keeps the first part; the rest is excluded here. A scope file is created for the second server, which you import there under",
    splitInfoPath: "Scopes → Import",
    splitInfoEnd: ".",
    share: "Share of this server (%)",
    shareHint: "typical: 80/20",
    thisServer: "This server",
    secondServer: "Second server",
    addresses: "({n} addresses)",
    splitLeases: "Existing leases in the second server's range expire here; the clients then get an address from their part.",
    splitDone: "Split: this server assigns {localStart} – {localEnd}, the second server {partnerStart} – {partnerEnd}.",
    splitNext: "Now download the scope file and import it on the second server. Options, reservations and policies are included.",
    download: "Download {file}",
    saved: "Scope saved",
    statistics: "Statistics",
    rangeSize: "Range size",
    excluded: "excluded",
    usable: "usable",
    inUse: "in use",
    reservedFree: "reserved (free)",
    blocked: "blocked",
    available: "available",
    settings: "Settings",
    name: "Name",
    subnet: "Subnet",
    subnetFixed: "The subnet of a scope cannot be changed",
    start: "Start address",
    end: "End address",
    leaseTime: "Lease duration",
    scopeActive: "Scope active",
    superscope: "Superscope",
    superscopeHint: "The scopes of a superscope together serve one physical network",
    noSuperscope: "– none –",
    bootp: "Serve BOOTP clients dynamically",
    description: "Description",
    exclusionCreated: "Exclusion created",
    exclusionRanges: "Exclusion ranges",
    exclusionHint: "Addresses are not assigned dynamically",
    from: "From",
    to: "To",
  },
  de: {
    split: "Split-Scope…",
    scopes: "Scopes",
    tabOverview: "Übersicht",
    tabExclusions: "Ausschlüsse",
    tabOptions: "Optionen",
    tabReservations: "Reservierungen",
    tabPolicies: "Policies",
    tabDns: "DNS",
    tabLeases: "Leases",
    scopeOptions: "Scope-Optionen",
    overrideServer: "überschreiben Server-Optionen",
    splitTitle: "Split-Scope – {name}",
    splitAction: "Aufteilen",
    splitInfo:
      "Teilt den dynamischen Bereich zwischen zwei unabhängigen Servern ohne Replikation auf (klassischer Fallback). Dieser Server behält den ersten Teil, der Rest wird hier ausgeschlossen. Für den zweiten Server entsteht eine Scope-Datei, die dort unter",
    splitInfoPath: "Scopes → Importieren",
    splitInfoEnd: " eingespielt wird.",
    share: "Anteil dieses Servers (%)",
    shareHint: "üblich: 80/20",
    thisServer: "Dieser Server",
    secondServer: "Zweiter Server",
    addresses: "({n} Adressen)",
    splitLeases: "Bestehende Leases im Bereich des zweiten Servers laufen hier aus; die Clients erhalten danach eine Adresse aus ihrem Teil.",
    splitDone: "Aufgeteilt: dieser Server vergibt {localStart} – {localEnd}, der zweite Server {partnerStart} – {partnerEnd}.",
    splitNext: "Jetzt die Scope-Datei herunterladen und auf dem zweiten Server importieren. Optionen, Reservierungen und Policies sind enthalten.",
    download: "{file} herunterladen",
    saved: "Scope gespeichert",
    statistics: "Statistik",
    rangeSize: "Bereichsgröße",
    excluded: "ausgeschlossen",
    usable: "nutzbar",
    inUse: "belegt",
    reservedFree: "reserviert (frei)",
    blocked: "gesperrt",
    available: "verfügbar",
    settings: "Einstellungen",
    name: "Name",
    subnet: "Subnetz",
    subnetFixed: "Das Subnetz eines Scopes ist unveränderlich",
    start: "Startadresse",
    end: "Endadresse",
    leaseTime: "Lease-Dauer",
    scopeActive: "Scope aktiv",
    superscope: "Superscope",
    superscopeHint: "Scopes eines Superscopes versorgen gemeinsam ein physisches Netz",
    noSuperscope: "– keiner –",
    bootp: "BOOTP-Clients dynamisch bedienen",
    description: "Beschreibung",
    exclusionCreated: "Ausschluss angelegt",
    exclusionRanges: "Ausschlussbereiche",
    exclusionHint: "Adressen werden nicht dynamisch vergeben",
    from: "Von",
    to: "Bis",
  },
});

type Tab = "overview" | "exclusions" | "options" | "reservations" | "policies" | "dns" | "leases";

export function ScopeDetailPage() {
  const id = Number(useParams().id);
  const { canWrite, haEnabled } = useAuth();
  const { toast } = useFeedback();
  const [tab, setTab] = useState<Tab>("overview");
  const [split, setSplit] = useState(false);
  const scope = useQuery({ queryKey: ["scope", id], queryFn: () => unwrap(api.GET("/scopes/{id}", { params: { path: { id } } })) });
  if (scope.isLoading) return <Spinner />;
  if (scope.error || !scope.data) return <Alert>{errorMessage(scope.error)}</Alert>;
  const s = scope.data;
  const exportScope = async () => {
    try {
      const b = await unwrap(api.GET("/scopes/{id}/export", { params: { path: { id } } }));
      downloadFile(`scope-${s.name.replace(/[^\w.-]+/g, "_")}.json`, JSON.stringify(b, null, 2));
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };
  return (
    <>
      <PageHeader
        actions={
          <div className="flex gap-2">
            <Button onClick={exportScope}>{c("export")}</Button>
            {canWrite && !haEnabled && <Button onClick={() => setSplit(true)}>{m("split")}</Button>}
          </div>
        }
        title={s.name}
        subtitle={
          <>
            <Link to="/scopes" className="text-teal-700 hover:underline dark:text-teal-400">
              {m("scopes")}
            </Link>{" "}
            / <span className="font-mono">{s.subnet}</span> {s.enabled ? <Badge color="green">{c("active")}</Badge> : <Badge color="amber">{c("disabled")}</Badge>}
          </>
        }
      />
      <Tabs<Tab>
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "overview", label: m("tabOverview") },
          { id: "exclusions", label: m("tabExclusions") },
          { id: "options", label: m("tabOptions") },
          { id: "reservations", label: m("tabReservations") },
          { id: "policies", label: m("tabPolicies") },
          { id: "dns", label: m("tabDns") },
          { id: "leases", label: m("tabLeases") },
        ]}
      />
      {tab === "overview" && <Overview scope={s} />}
      {tab === "exclusions" && <Exclusions scope={s} />}
      {tab === "options" && (
        <Card title={m("scopeOptions")} actions={<span className="text-xs text-slate-500">{m("overrideServer")}</span>}>
          <OptionsEditor owner="scope" ownerId={s.id} />
        </Card>
      )}
      {tab === "reservations" && <ReservationsTable scopeId={s.id} />}
      {tab === "policies" && <PoliciesList scopeId={s.id} />}
      {tab === "dns" && <ScopeDNS scope={s} />}
      {tab === "leases" && <LeasesTable scopeId={s.id} />}
      {split && <SplitWizard scope={s} onClose={() => setSplit(false)} />}
    </>
  );
}

/** Classic split scope (two independent servers, no replication). */
function SplitWizard({ scope, onClose }: { scope: S["Scope"]; onClose: () => void }) {
  const qc = useQueryClient();
  const [pct, setPct] = useState(80);
  const [result, setResult] = useState<S["SplitScopeResult"] | null>(null);
  const [err, setErr] = useState("");
  const start = parseIPv4(scope.start) ?? 0;
  const end = parseIPv4(scope.end) ?? 0;
  const size = end - start + 1;
  const n = Math.max(1, Math.min(size - 1, Math.floor((size * pct) / 100)));
  const split = useMutation({
    mutationFn: () => unwrap(api.POST("/scopes/{id}/split", { params: { path: { id: scope.id } }, body: { local_percent: pct } })),
    onSuccess: (r) => {
      setResult(r);
      setErr("");
      qc.invalidateQueries({ queryKey: ["exclusions", scope.id] });
      qc.invalidateQueries({ queryKey: ["scope-stats", scope.id] });
      qc.invalidateQueries({ queryKey: ["stats"] });
    },
    onError: (e) => setErr(errorMessage(e)),
  });
  const file = `scope-${scope.name.replace(/[^\w.-]+/g, "_")}-partner.json`;
  return (
    <Modal
      title={m("splitTitle", { name: scope.name })}
      onClose={onClose}
      wide
      footer={
        result ? (
          <Button variant="primary" onClick={onClose}>
            {c("done")}
          </Button>
        ) : (
          <>
            <Button onClick={onClose}>{c("cancel")}</Button>
            <Button variant="primary" disabled={split.isPending || pct < 1 || pct > 99} onClick={() => split.mutate()}>
              {m("splitAction")}
            </Button>
          </>
        )
      }
    >
      {!result ? (
        <>
          <p className="text-sm text-slate-600 dark:text-slate-400">
            {m("splitInfo")} <em>{m("splitInfoPath")}</em>
            {m("splitInfoEnd")}
          </p>
          <Input
            label={m("share")}
            type="number"
            min={1}
            max={99}
            value={String(pct)}
            onChange={(e) => setPct(Number(e.target.value))}
            hint={m("shareHint")}
          />
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1 text-sm">
            <dt className="text-slate-500">{m("thisServer")}</dt>
            <dd className="font-mono">
              {formatIPv4(start)} – {formatIPv4(start + n - 1)} {m("addresses", { n })}
            </dd>
            <dt className="text-slate-500">{m("secondServer")}</dt>
            <dd className="font-mono">
              {formatIPv4(start + n)} – {formatIPv4(end)} {m("addresses", { n: size - n })}
            </dd>
          </dl>
          <p className="text-xs text-slate-500">{m("splitLeases")}</p>
          {err && <Alert>{err}</Alert>}
        </>
      ) : (
        <>
          <Alert kind="success">
            {m("splitDone", {
              localStart: result.local_range.start,
              localEnd: result.local_range.end,
              partnerStart: result.partner_range.start,
              partnerEnd: result.partner_range.end,
            })}
          </Alert>
          <p className="text-sm">{m("splitNext")}</p>
          <div>
            <Button variant="primary" onClick={() => downloadFile(file, JSON.stringify(result.partner, null, 2))}>
              {m("download", { file })}
            </Button>
          </div>
        </>
      )}
    </Modal>
  );
}

function Overview({ scope }: { scope: S["Scope"] }) {
  const { canWrite } = useAuth();
  const stats = useQuery({
    queryKey: ["scope-stats", scope.id],
    queryFn: () => unwrap(api.GET("/scopes/{id}/stats", { params: { path: { id: scope.id } } })),
    refetchInterval: 5000,
  });
  const scopes = useScopes();
  const superscopes = useSuperscopes();
  const qc = useQueryClient();
  const { toast } = useFeedback();
  const [f, setF] = useState(scope);
  const [errors, setErrors] = useState<Errors>({});
  useEffect(() => setF(scope), [scope]);

  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT("/scopes/{id}", {
          params: { path: { id: scope.id } },
          body: {
            name: f.name,
            description: f.description,
            subnet: f.subnet,
            start: f.start,
            end: f.end,
            lease_time: f.lease_time,
            enabled: f.enabled,
            bootp: f.bootp,
            superscope_id: f.superscope_id,
          },
        }),
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["scope", scope.id] });
      qc.invalidateQueries({ queryKey: ["scopes"] });
      qc.invalidateQueries({ queryKey: ["superscopes"] });
      toast(m("saved"));
    },
    onError: (e) => {
      if (e instanceof ApiError) setErrors(e.fields);
      toast(errorMessage(e), "error");
    },
  });

  const submit = () => {
    const e = validateScope(f, (scopes.data ?? []).filter((x) => x.id !== scope.id));
    setErrors(e);
    if (!hasErrors(e)) save.mutate();
  };
  const st = stats.data;
  const times = leaseTimes();

  return (
    <div className="grid gap-6 lg:grid-cols-[2fr_3fr]">
      <Card title={m("statistics")}>
        {st ? (
          <div className="space-y-3 text-sm">
            <Progress percent={st.utilization_percent} />
            <dl className="grid grid-cols-2 gap-y-1">
              <dt className="text-slate-500">{m("rangeSize")}</dt>
              <dd className="tabular-nums">{st.total}</dd>
              <dt className="text-slate-500">{m("excluded")}</dt>
              <dd className="tabular-nums">{st.excluded}</dd>
              <dt className="text-slate-500">{m("usable")}</dt>
              <dd className="tabular-nums">{st.usable}</dd>
              <dt className="text-slate-500">{m("inUse")}</dt>
              <dd className="tabular-nums">{st.in_use}</dd>
              <dt className="text-slate-500">{m("reservedFree")}</dt>
              <dd className="tabular-nums">{st.reserved}</dd>
              <dt className="text-slate-500">{m("blocked")}</dt>
              <dd className="tabular-nums">{st.declined}</dd>
              <dt className="text-slate-500">{m("available")}</dt>
              <dd className="font-semibold tabular-nums">{st.available}</dd>
            </dl>
          </div>
        ) : (
          <Spinner />
        )}
      </Card>
      <Card title={m("settings")}>
        <form
          className="grid gap-4 sm:grid-cols-2"
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          <Input label={m("name")} value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} error={errors.name} disabled={!canWrite} />
          <Input label={m("subnet")} value={f.subnet} disabled hint={m("subnetFixed")} />
          <Input label={m("start")} value={f.start} onChange={(e) => setF({ ...f, start: e.target.value })} error={errors.start} disabled={!canWrite} />
          <Input label={m("end")} value={f.end} onChange={(e) => setF({ ...f, end: e.target.value })} error={errors.end} disabled={!canWrite} />
          <Select
            label={m("leaseTime")}
            value={String(f.lease_time)}
            onChange={(e) => setF({ ...f, lease_time: Number(e.target.value) })}
            error={errors.lease_time}
            disabled={!canWrite}
          >
            {[...times, ...(times.some((t) => t.v === f.lease_time) ? [] : [{ v: f.lease_time, l: formatDuration(f.lease_time) }])].map((t) => (
              <option key={t.v} value={t.v}>
                {t.l}
              </option>
            ))}
          </Select>
          <div className="flex items-end pb-2">
            <Toggle label={m("scopeActive")} checked={f.enabled} onChange={(v) => setF({ ...f, enabled: v })} disabled={!canWrite} />
          </div>
          <Select
            label={m("superscope")}
            value={String(f.superscope_id)}
            onChange={(e) => setF({ ...f, superscope_id: Number(e.target.value) })}
            disabled={!canWrite}
            hint={m("superscopeHint")}
          >
            <option value="0">{m("noSuperscope")}</option>
            {superscopes.data?.map((ss) => (
              <option key={ss.id} value={ss.id}>
                {ss.name}
              </option>
            ))}
          </Select>
          <div className="flex items-end pb-2">
            <Toggle label={m("bootp")} checked={f.bootp} onChange={(v) => setF({ ...f, bootp: v })} disabled={!canWrite} />
          </div>
          <div className="sm:col-span-2">
            <TextArea label={m("description")} rows={2} value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} disabled={!canWrite} />
          </div>
          {canWrite && (
            <div className="sm:col-span-2">
              <Button type="submit" variant="primary" disabled={save.isPending}>
                {c("save")}
              </Button>
            </div>
          )}
        </form>
      </Card>
    </div>
  );
}

function Exclusions({ scope }: { scope: S["Scope"] }) {
  const { canWrite } = useAuth();
  const qc = useQueryClient();
  const { toast } = useFeedback();
  const key = ["exclusions", scope.id];
  const ex = useQuery({ queryKey: key, queryFn: () => unwrap(api.GET("/scopes/{id}/exclusions", { params: { path: { id: scope.id } } })) });
  const [start, setStart] = useState("");
  const [end, setEnd] = useState("");
  const [errors, setErrors] = useState<Errors>({});
  const refresh = () => {
    qc.invalidateQueries({ queryKey: key });
    qc.invalidateQueries({ queryKey: ["scope-stats", scope.id] });
  };
  const add = useMutation({
    mutationFn: () => unwrap(api.POST("/scopes/{id}/exclusions", { params: { path: { id: scope.id } }, body: { start, end } })),
    onSuccess: () => {
      refresh();
      setStart("");
      setEnd("");
      toast(m("exclusionCreated"));
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const del = useMutation({
    mutationFn: (eid: number) => unwrap(api.DELETE("/scopes/{id}/exclusions/{exclusionId}", { params: { path: { id: scope.id, exclusionId: eid } } })),
    onSuccess: refresh,
    onError: (e) => toast(errorMessage(e), "error"),
  });
  return (
    <Card title={m("exclusionRanges")} actions={<span className="text-xs text-slate-500">{m("exclusionHint")}</span>}>
      <Table head={[m("from"), m("to"), ""]} empty={ex.data?.length === 0}>
        {ex.data?.map((e) => (
          <tr key={e.id}>
            <Td mono>{e.start}</Td>
            <Td mono>{e.end}</Td>
            <Td className="text-right">
              {canWrite && (
                <Button size="sm" variant="ghost" onClick={() => del.mutate(e.id)}>
                  {c("remove")}
                </Button>
              )}
            </Td>
          </tr>
        ))}
      </Table>
      {canWrite && (
        <form
          className="mt-4 grid gap-3 sm:grid-cols-[1fr_1fr_auto] sm:items-end"
          onSubmit={(e) => {
            e.preventDefault();
            const v = validateRange(start, end, scope);
            setErrors(v);
            if (!hasErrors(v)) add.mutate();
          }}
        >
          <Input label={m("from")} value={start} onChange={(e) => setStart(e.target.value)} error={errors.start} />
          <Input label={m("to")} value={end} onChange={(e) => setEnd(e.target.value)} error={errors.end} />
          <Button type="submit" variant="primary">
            {c("add")}
          </Button>
        </form>
      )}
    </Card>
  );
}

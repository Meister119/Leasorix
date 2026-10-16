import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "react-router";
import { api, ApiError, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { OptionsEditor } from "../components/OptionsEditor";
import { Alert, Badge, Button, Card, Input, Modal, PageHeader, Progress, Select, Spinner, Table, Tabs, Td, TextArea, Toggle, useFeedback } from "../components/ui";
import { downloadFile } from "../lib/download";
import { formatDuration } from "../lib/format";
import { formatIPv4, parseIPv4 } from "../lib/validate";
import { hasErrors, validateRange, validateScope, type Errors } from "../lib/validate";
import { LeasesTable } from "./Leases";
import { PoliciesList } from "./Policies";
import { ReservationsTable } from "./Reservations";
import { leaseTimes, useScopes, useSuperscopes } from "./Scopes";

type Tab = "overview" | "exclusions" | "options" | "reservations" | "policies" | "leases";

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
            <Button onClick={exportScope}>Exportieren</Button>
            {canWrite && !haEnabled && <Button onClick={() => setSplit(true)}>Split-Scope…</Button>}
          </div>
        }
        title={s.name}
        subtitle={
          <>
            <Link to="/scopes" className="text-teal-700 hover:underline dark:text-teal-400">
              Scopes
            </Link>{" "}
            / <span className="font-mono">{s.subnet}</span> {s.enabled ? <Badge color="green">aktiv</Badge> : <Badge color="amber">deaktiviert</Badge>}
          </>
        }
      />
      <Tabs<Tab>
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "overview", label: "Übersicht" },
          { id: "exclusions", label: "Ausschlüsse" },
          { id: "options", label: "Optionen" },
          { id: "reservations", label: "Reservierungen" },
          { id: "policies", label: "Policies" },
          { id: "leases", label: "Leases" },
        ]}
      />
      {tab === "overview" && <Overview scope={s} />}
      {tab === "exclusions" && <Exclusions scope={s} />}
      {tab === "options" && (
        <Card title="Scope-Optionen" actions={<span className="text-xs text-slate-500">überschreiben Server-Optionen</span>}>
          <OptionsEditor owner="scope" ownerId={s.id} />
        </Card>
      )}
      {tab === "reservations" && <ReservationsTable scopeId={s.id} />}
      {tab === "policies" && <PoliciesList scopeId={s.id} />}
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
      title={`Split-Scope – ${scope.name}`}
      onClose={onClose}
      wide
      footer={
        result ? (
          <Button variant="primary" onClick={onClose}>
            Fertig
          </Button>
        ) : (
          <>
            <Button onClick={onClose}>Abbrechen</Button>
            <Button variant="primary" disabled={split.isPending || pct < 1 || pct > 99} onClick={() => split.mutate()}>
              Aufteilen
            </Button>
          </>
        )
      }
    >
      {!result ? (
        <>
          <p className="text-sm text-slate-600 dark:text-slate-400">
            Teilt den dynamischen Bereich zwischen zwei unabhängigen Servern ohne Replikation auf (klassischer Fallback). Dieser Server behält den
            ersten Teil, der Rest wird hier ausgeschlossen. Für den zweiten Server entsteht eine Scope-Datei, die dort unter{" "}
            <em>Scopes → Importieren</em> eingespielt wird.
          </p>
          <Input
            label="Anteil dieses Servers (%)"
            type="number"
            min={1}
            max={99}
            value={String(pct)}
            onChange={(e) => setPct(Number(e.target.value))}
            hint={`üblich: 80/20`}
          />
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1 text-sm">
            <dt className="text-slate-500">Dieser Server</dt>
            <dd className="font-mono">
              {formatIPv4(start)} – {formatIPv4(start + n - 1)} ({n} Adressen)
            </dd>
            <dt className="text-slate-500">Zweiter Server</dt>
            <dd className="font-mono">
              {formatIPv4(start + n)} – {formatIPv4(end)} ({size - n} Adressen)
            </dd>
          </dl>
          <p className="text-xs text-slate-500">
            Bestehende Leases im Bereich des zweiten Servers laufen hier aus; die Clients erhalten danach eine Adresse aus ihrem Teil.
          </p>
          {err && <Alert>{err}</Alert>}
        </>
      ) : (
        <>
          <Alert kind="success">
            Aufgeteilt: dieser Server vergibt {result.local_range.start} – {result.local_range.end}, der zweite Server {result.partner_range.start} –{" "}
            {result.partner_range.end}.
          </Alert>
          <p className="text-sm">
            Jetzt die Scope-Datei herunterladen und auf dem zweiten Server importieren. Optionen, Reservierungen und Policies sind enthalten.
          </p>
          <div>
            <Button variant="primary" onClick={() => downloadFile(file, JSON.stringify(result.partner, null, 2))}>
              {file} herunterladen
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
      toast("Scope gespeichert");
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

  return (
    <div className="grid gap-6 lg:grid-cols-[2fr_3fr]">
      <Card title="Statistik">
        {st ? (
          <div className="space-y-3 text-sm">
            <Progress percent={st.utilization_percent} />
            <dl className="grid grid-cols-2 gap-y-1">
              <dt className="text-slate-500">Bereichsgröße</dt>
              <dd className="tabular-nums">{st.total}</dd>
              <dt className="text-slate-500">ausgeschlossen</dt>
              <dd className="tabular-nums">{st.excluded}</dd>
              <dt className="text-slate-500">nutzbar</dt>
              <dd className="tabular-nums">{st.usable}</dd>
              <dt className="text-slate-500">belegt</dt>
              <dd className="tabular-nums">{st.in_use}</dd>
              <dt className="text-slate-500">reserviert (frei)</dt>
              <dd className="tabular-nums">{st.reserved}</dd>
              <dt className="text-slate-500">gesperrt</dt>
              <dd className="tabular-nums">{st.declined}</dd>
              <dt className="text-slate-500">verfügbar</dt>
              <dd className="font-semibold tabular-nums">{st.available}</dd>
            </dl>
          </div>
        ) : (
          <Spinner />
        )}
      </Card>
      <Card title="Einstellungen">
        <form
          className="grid gap-4 sm:grid-cols-2"
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          <Input label="Name" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} error={errors.name} disabled={!canWrite} />
          <Input label="Subnetz" value={f.subnet} disabled hint="Das Subnetz eines Scopes ist unveränderlich" />
          <Input label="Startadresse" value={f.start} onChange={(e) => setF({ ...f, start: e.target.value })} error={errors.start} disabled={!canWrite} />
          <Input label="Endadresse" value={f.end} onChange={(e) => setF({ ...f, end: e.target.value })} error={errors.end} disabled={!canWrite} />
          <Select
            label="Lease-Dauer"
            value={String(f.lease_time)}
            onChange={(e) => setF({ ...f, lease_time: Number(e.target.value) })}
            error={errors.lease_time}
            disabled={!canWrite}
          >
            {[...leaseTimes, ...(leaseTimes.some((t) => t.v === f.lease_time) ? [] : [{ v: f.lease_time, l: formatDuration(f.lease_time) }])].map((t) => (
              <option key={t.v} value={t.v}>
                {t.l}
              </option>
            ))}
          </Select>
          <div className="flex items-end pb-2">
            <Toggle label="Scope aktiv" checked={f.enabled} onChange={(v) => setF({ ...f, enabled: v })} disabled={!canWrite} />
          </div>
          <Select
            label="Superscope"
            value={String(f.superscope_id)}
            onChange={(e) => setF({ ...f, superscope_id: Number(e.target.value) })}
            disabled={!canWrite}
            hint="Scopes eines Superscopes versorgen gemeinsam ein physisches Netz"
          >
            <option value="0">– keiner –</option>
            {superscopes.data?.map((ss) => (
              <option key={ss.id} value={ss.id}>
                {ss.name}
              </option>
            ))}
          </Select>
          <div className="flex items-end pb-2">
            <Toggle label="BOOTP-Clients dynamisch bedienen" checked={f.bootp} onChange={(v) => setF({ ...f, bootp: v })} disabled={!canWrite} />
          </div>
          <div className="sm:col-span-2">
            <TextArea label="Beschreibung" rows={2} value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} disabled={!canWrite} />
          </div>
          {canWrite && (
            <div className="sm:col-span-2">
              <Button type="submit" variant="primary" disabled={save.isPending}>
                Speichern
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
      toast("Ausschluss angelegt");
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const del = useMutation({
    mutationFn: (eid: number) => unwrap(api.DELETE("/scopes/{id}/exclusions/{exclusionId}", { params: { path: { id: scope.id, exclusionId: eid } } })),
    onSuccess: refresh,
    onError: (e) => toast(errorMessage(e), "error"),
  });
  return (
    <Card title="Ausschlussbereiche" actions={<span className="text-xs text-slate-500">Adressen werden nicht dynamisch vergeben</span>}>
      <Table head={["Von", "Bis", ""]} empty={ex.data?.length === 0}>
        {ex.data?.map((e) => (
          <tr key={e.id}>
            <Td mono>{e.start}</Td>
            <Td mono>{e.end}</Td>
            <Td className="text-right">
              {canWrite && (
                <Button size="sm" variant="ghost" onClick={() => del.mutate(e.id)}>
                  Entfernen
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
          <Input label="Von" value={start} onChange={(e) => setStart(e.target.value)} error={errors.start} />
          <Input label="Bis" value={end} onChange={(e) => setEnd(e.target.value)} error={errors.end} />
          <Button type="submit" variant="primary">
            Hinzufügen
          </Button>
        </form>
      )}
    </Card>
  );
}

import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "react-router";
import { api, ApiError, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { OptionsEditor } from "../components/OptionsEditor";
import { Alert, Badge, Button, Card, Input, PageHeader, Progress, Select, Spinner, Table, Tabs, Td, TextArea, Toggle, useFeedback } from "../components/ui";
import { formatDuration } from "../lib/format";
import { hasErrors, validateRange, validateScope, type Errors } from "../lib/validate";
import { LeasesTable } from "./Leases";
import { PoliciesList } from "./Policies";
import { ReservationsTable } from "./Reservations";
import { leaseTimes, useScopes } from "./Scopes";

type Tab = "overview" | "exclusions" | "options" | "reservations" | "policies" | "leases";

export function ScopeDetailPage() {
  const id = Number(useParams().id);
  const [tab, setTab] = useState<Tab>("overview");
  const scope = useQuery({ queryKey: ["scope", id], queryFn: () => unwrap(api.GET("/scopes/{id}", { params: { path: { id } } })) });
  if (scope.isLoading) return <Spinner />;
  if (scope.error || !scope.data) return <Alert>{errorMessage(scope.error)}</Alert>;
  const s = scope.data;
  return (
    <>
      <PageHeader
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
    </>
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
          body: { name: f.name, description: f.description, subnet: f.subnet, start: f.start, end: f.end, lease_time: f.lease_time, enabled: f.enabled },
        }),
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["scope", scope.id] });
      qc.invalidateQueries({ queryKey: ["scopes"] });
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

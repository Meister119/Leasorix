import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { OptionsEditor } from "../components/OptionsEditor";
import { Alert, Badge, Button, Card, Input, Modal, PageHeader, Select, Spinner, Table, Tabs, Td, Toggle, useFeedback } from "../components/ui";
import { formatDuration } from "../lib/format";
import { hasErrors, validateRange, validMACPattern, type Errors } from "../lib/validate";
import { useScopes } from "./Scopes";

type Tab = "policies" | "classes" | "filters";

export function PoliciesPage() {
  const [tab, setTab] = useState<Tab>("policies");
  return (
    <>
      <PageHeader title="Policies & Filter" subtitle="Bedingte Optionen und Adressbereiche, Klassen, MAC-Filter" />
      <Tabs<Tab>
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "policies", label: "Server-Policies" },
          { id: "classes", label: "Vendor-/User-Klassen" },
          { id: "filters", label: "MAC-Filter" },
        ]}
      />
      {tab === "policies" && (
        <>
          <Alert kind="info">Scope-Policies werden in der Detailansicht des jeweiligen Scopes gepflegt. Server-Policies gelten für alle Scopes.</Alert>
          <div className="mt-4">
            <PoliciesList scopeId={0} />
          </div>
        </>
      )}
      {tab === "classes" && <Classes />}
      {tab === "filters" && <Filters />}
    </>
  );
}

const fieldLabels: Record<S["ConditionField"], string> = {
  mac: "MAC-Adresse",
  client_id: "Client-ID",
  hostname: "Hostname",
  vendor_class: "Vendor-Class (Opt. 60)",
  user_class: "User-Class (Opt. 77)",
  class: "Definierte Klasse",
  relay_circuit_id: "Relay Circuit-ID (82.1)",
  relay_remote_id: "Relay Remote-ID (82.2)",
  relay_subscriber_id: "Relay Subscriber-ID (82.6)",
};

/** Policies of one level: scopeId 0 = server level. */
export function PoliciesList({ scopeId }: { scopeId: number }) {
  const { canWrite } = useAuth();
  const qc = useQueryClient();
  const { toast, confirm } = useFeedback();
  const key = ["policies", scopeId];
  const list = useQuery({ queryKey: key, queryFn: () => unwrap(api.GET("/policies", { params: { query: { scope_id: scopeId } } })) });
  const [edit, setEdit] = useState<S["Policy"] | "new" | null>(null);
  const del = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/policies/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["policies"] });
      toast("Policy gelöscht");
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  if (list.isLoading) return <Spinner />;
  return (
    <Card
      title={scopeId ? "Scope-Policies" : "Server-Policies"}
      actions={
        canWrite && (
          <Button variant="primary" size="sm" onClick={() => setEdit("new")}>
            Neue Policy
          </Button>
        )
      }
    >
      <Table head={["Reihenfolge", "Name", "Bedingungen", "Bereiche", "Lease-Dauer", "Status", ""]} empty={list.data?.length === 0}>
        {list.data?.map((p) => (
          <tr key={p.id}>
            <Td className="tabular-nums">{p.order}</Td>
            <Td className="font-medium">{p.name}</Td>
            <Td className="max-w-sm truncate whitespace-normal">
              {p.conditions.map((c, i) => (
                <span key={i} className="mr-1 text-xs">
                  {i > 0 && <b className="mr-1">{p.operator === "and" ? "UND" : "ODER"}</b>}
                  {fieldLabels[c.field]} {c.operator === "eq" ? "=" : "≠"} <code>{c.value}</code>
                </span>
              ))}
            </Td>
            <Td mono>{p.ranges.map((r) => `${r.start}–${r.end}`).join(", ") || "–"}</Td>
            <Td>{p.lease_time == null ? "geerbt" : formatDuration(p.lease_time)}</Td>
            <Td>{p.enabled ? <Badge color="green">aktiv</Badge> : <Badge>aus</Badge>}</Td>
            <Td className="text-right">
              <div className="flex justify-end gap-1">
                <Button size="sm" variant="ghost" onClick={() => setEdit(p)}>
                  {canWrite ? "Bearbeiten" : "Details"}
                </Button>
                {canWrite && (
                  <Button size="sm" variant="ghost" onClick={async () => (await confirm(`Policy „${p.name}“ löschen?`, { danger: true })) && del.mutate(p.id)}>
                    Löschen
                  </Button>
                )}
              </div>
            </Td>
          </tr>
        ))}
      </Table>
      {edit && <PolicyEditor scopeId={scopeId} policy={edit === "new" ? undefined : edit} onClose={() => setEdit(null)} />}
    </Card>
  );
}

function PolicyEditor({ scopeId, policy, onClose }: { scopeId: number; policy?: S["Policy"]; onClose: () => void }) {
  const { canWrite } = useAuth();
  const scopes = useScopes();
  const classes = useQuery({ queryKey: ["classes"], queryFn: () => unwrap(api.GET("/classes")) });
  const scope = scopes.data?.find((s) => s.id === scopeId);
  const qc = useQueryClient();
  const { toast } = useFeedback();
  const [p, setP] = useState({
    name: policy?.name ?? "",
    description: policy?.description ?? "",
    order: policy?.order ?? 0,
    enabled: policy?.enabled ?? true,
    operator: policy?.operator ?? ("and" as "and" | "or"),
    lease: policy?.lease_time == null ? "" : String(policy.lease_time),
    conditions: policy?.conditions ?? [{ field: "vendor_class" as S["ConditionField"], operator: "eq" as const, value: "" }],
    ranges: policy?.ranges ?? [],
  });
  const [errors, setErrors] = useState<Errors>({});
  const [error, setError] = useState("");
  const [saved, setSaved] = useState<S["Policy"] | undefined>(policy);

  const save = useMutation({
    mutationFn: () => {
      const body = {
        scope_id: scopeId,
        name: p.name,
        description: p.description,
        order: p.order,
        enabled: p.enabled,
        operator: p.operator,
        lease_time: p.lease === "" ? null : Number(p.lease),
        conditions: p.conditions,
        ranges: p.ranges,
      };
      return saved
        ? unwrap(api.PUT("/policies/{id}", { params: { path: { id: saved.id } }, body }))
        : unwrap(api.POST("/policies", { body }));
    },
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ["policies"] });
      toast("Policy gespeichert");
      setSaved(r);
      setError("");
    },
    onError: (e) => {
      if (e instanceof ApiError) setErrors(e.fields);
      setError(errorMessage(e));
    },
  });

  const validate = () => {
    const e: Errors = {};
    if (!p.name.trim()) e.name = "Name ist erforderlich";
    if (p.conditions.length === 0) e.conditions = "Mindestens eine Bedingung";
    p.conditions.forEach((c, i) => {
      if (!c.value.trim()) e[`conditions[${i}].value`] = "Wert erforderlich";
      else if (c.field === "mac" && !validMACPattern(c.value)) e[`conditions[${i}].value`] = "MAC oder Muster wie 00:15:5d:*";
    });
    p.ranges.forEach((r, i) => {
      const re = validateRange(r.start, r.end, scope ? { start: scope.start, end: scope.end } : undefined);
      if (hasErrors(re)) e[`ranges[${i}]`] = Object.values(re)[0];
    });
    if (p.lease !== "" && (!/^\d+$/.test(p.lease) || (Number(p.lease) !== 0 && Number(p.lease) < 10))) e.lease_time = "0 oder mindestens 10 Sekunden";
    setErrors(e);
    return !hasErrors(e);
  };

  const setCond = (i: number, c: Partial<S["Condition"]>) => setP({ ...p, conditions: p.conditions.map((x, j) => (j === i ? { ...x, ...c } : x)) });

  return (
    <Modal
      title={saved ? `Policy „${saved.name}“` : "Neue Policy"}
      onClose={onClose}
      wide
      footer={
        canWrite && (
          <Button variant="primary" onClick={() => validate() && save.mutate()} disabled={save.isPending}>
            Speichern
          </Button>
        )
      }
    >
      {error && <Alert>{error}</Alert>}
      <div className="grid gap-4 sm:grid-cols-3">
        <Input label="Name" value={p.name} onChange={(e) => setP({ ...p, name: e.target.value })} error={errors.name} />
        <Input label="Reihenfolge" type="number" min={0} value={p.order} onChange={(e) => setP({ ...p, order: Number(e.target.value) })} hint="0 = ans Ende" />
        <Input label="Lease-Dauer (s)" value={p.lease} onChange={(e) => setP({ ...p, lease: e.target.value })} error={errors.lease_time} hint="leer = vom Scope" />
      </div>
      <Input label="Beschreibung" value={p.description} onChange={(e) => setP({ ...p, description: e.target.value })} />
      <Toggle label="Policy aktiv" checked={p.enabled} onChange={(v) => setP({ ...p, enabled: v })} />

      <fieldset className="space-y-2">
        <legend className="mb-1 flex items-center gap-3 text-sm font-medium">
          Bedingungen
          <select
            aria-label="Verknüpfung"
            value={p.operator}
            onChange={(e) => setP({ ...p, operator: e.target.value as "and" | "or" })}
            className="rounded-md border border-slate-300 bg-white px-2 py-1 text-xs dark:border-slate-700 dark:bg-slate-900"
          >
            <option value="and">alle müssen zutreffen (UND)</option>
            <option value="or">eine muss zutreffen (ODER)</option>
          </select>
        </legend>
        {errors.conditions && <p className="text-xs text-red-600">{errors.conditions}</p>}
        {p.conditions.map((c, i) => (
          <div key={i} className="grid gap-2 sm:grid-cols-[1.4fr_0.7fr_2fr_auto]">
            <select
              aria-label="Feld"
              value={c.field}
              onChange={(e) => setCond(i, { field: e.target.value as S["ConditionField"] })}
              className="rounded-md border border-slate-300 bg-white px-2 py-2 text-sm dark:border-slate-700 dark:bg-slate-900"
            >
              {Object.entries(fieldLabels).map(([k, v]) => (
                <option key={k} value={k}>
                  {v}
                </option>
              ))}
            </select>
            <select
              aria-label="Operator"
              value={c.operator}
              onChange={(e) => setCond(i, { operator: e.target.value as "eq" | "ne" })}
              className="rounded-md border border-slate-300 bg-white px-2 py-2 text-sm dark:border-slate-700 dark:bg-slate-900"
            >
              <option value="eq">ist</option>
              <option value="ne">ist nicht</option>
            </select>
            {c.field === "class" ? (
              <select
                aria-label="Klasse"
                value={c.value}
                onChange={(e) => setCond(i, { value: e.target.value })}
                className="rounded-md border border-slate-300 bg-white px-2 py-2 text-sm dark:border-slate-700 dark:bg-slate-900"
              >
                <option value="">– Klasse –</option>
                {classes.data?.map((k) => (
                  <option key={k.id} value={k.name}>
                    {k.name} ({k.type})
                  </option>
                ))}
              </select>
            ) : (
              <div>
                <input
                  aria-label="Wert"
                  value={c.value}
                  placeholder="Wert, Wildcards * und ?, 0x… für Hex"
                  onChange={(e) => setCond(i, { value: e.target.value })}
                  className={`w-full rounded-md border bg-white px-2 py-2 text-sm dark:bg-slate-900 ${errors[`conditions[${i}].value`] ? "border-red-500" : "border-slate-300 dark:border-slate-700"}`}
                />
                {errors[`conditions[${i}].value`] && <p className="text-xs text-red-600">{errors[`conditions[${i}].value`]}</p>}
              </div>
            )}
            <Button size="sm" variant="ghost" onClick={() => setP({ ...p, conditions: p.conditions.filter((_, j) => j !== i) })}>
              Entfernen
            </Button>
          </div>
        ))}
        <Button size="sm" onClick={() => setP({ ...p, conditions: [...p.conditions, { field: "mac", operator: "eq", value: "" }] })}>
          Bedingung hinzufügen
        </Button>
      </fieldset>

      {scopeId > 0 && (
        <fieldset className="space-y-2">
          <legend className="mb-1 text-sm font-medium">Eigene Adressbereiche (nur passende Clients)</legend>
          {p.ranges.map((r, i) => (
            <div key={i} className="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
              <input
                aria-label="Bereich von"
                value={r.start}
                onChange={(e) => setP({ ...p, ranges: p.ranges.map((x, j) => (j === i ? { ...x, start: e.target.value } : x)) })}
                className="rounded-md border border-slate-300 bg-white px-2 py-2 font-mono text-sm dark:border-slate-700 dark:bg-slate-900"
              />
              <input
                aria-label="Bereich bis"
                value={r.end}
                onChange={(e) => setP({ ...p, ranges: p.ranges.map((x, j) => (j === i ? { ...x, end: e.target.value } : x)) })}
                className="rounded-md border border-slate-300 bg-white px-2 py-2 font-mono text-sm dark:border-slate-700 dark:bg-slate-900"
              />
              <Button size="sm" variant="ghost" onClick={() => setP({ ...p, ranges: p.ranges.filter((_, j) => j !== i) })}>
                Entfernen
              </Button>
              {errors[`ranges[${i}]`] && <p className="text-xs text-red-600 sm:col-span-3">{errors[`ranges[${i}]`]}</p>}
            </div>
          ))}
          <Button size="sm" onClick={() => setP({ ...p, ranges: [...p.ranges, { start: "", end: "" }] })}>
            Bereich hinzufügen
          </Button>
        </fieldset>
      )}

      {saved ? (
        <Card title="Optionen dieser Policy">
          <OptionsEditor owner="policy" ownerId={saved.id} />
        </Card>
      ) : (
        <p className="text-xs text-slate-500">Optionen können nach dem ersten Speichern festgelegt werden.</p>
      )}
    </Modal>
  );
}

function Classes() {
  const { canWrite } = useAuth();
  const qc = useQueryClient();
  const { toast, confirm } = useFeedback();
  const list = useQuery({ queryKey: ["classes"], queryFn: () => unwrap(api.GET("/classes")) });
  const [f, setF] = useState({ name: "", type: "vendor" as "vendor" | "user", data: "", description: "" });
  const add = useMutation({
    mutationFn: () => unwrap(api.POST("/classes", { body: f })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["classes"] });
      setF({ name: "", type: "vendor", data: "", description: "" });
      toast("Klasse angelegt");
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const del = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/classes/{id}", { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["classes"] }),
    onError: (e) => toast(errorMessage(e), "error"),
  });
  return (
    <Card title="Klassen">
      <Table head={["Name", "Typ", "Muster", "Beschreibung", ""]} empty={list.data?.length === 0}>
        {list.data?.map((c) => (
          <tr key={c.id}>
            <Td className="font-medium">{c.name}</Td>
            <Td>
              <Badge color={c.type === "vendor" ? "blue" : "teal"}>{c.type === "vendor" ? "Vendor (60)" : "User (77)"}</Badge>
            </Td>
            <Td mono>{c.data}</Td>
            <Td>{c.description}</Td>
            <Td className="text-right">
              {canWrite && (
                <Button size="sm" variant="ghost" onClick={async () => (await confirm(`Klasse „${c.name}“ löschen?`, { danger: true })) && del.mutate(c.id)}>
                  Löschen
                </Button>
              )}
            </Td>
          </tr>
        ))}
      </Table>
      {canWrite && (
        <form
          className="mt-4 grid gap-3 sm:grid-cols-[1fr_1fr_1.5fr_1.5fr_auto] sm:items-end"
          onSubmit={(e) => {
            e.preventDefault();
            add.mutate();
          }}
        >
          <Input label="Name" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} required />
          <Select label="Typ" value={f.type} onChange={(e) => setF({ ...f, type: e.target.value as "vendor" | "user" })}>
            <option value="vendor">Vendor-Class</option>
            <option value="user">User-Class</option>
          </Select>
          <Input label="Muster" value={f.data} onChange={(e) => setF({ ...f, data: e.target.value })} placeholder="PXEClient*" required />
          <Input label="Beschreibung" value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} />
          <Button type="submit" variant="primary">
            Anlegen
          </Button>
        </form>
      )}
    </Card>
  );
}

function Filters() {
  const { canWrite, isAdmin: admin, configReadOnly } = useAuth();
  const isAdmin = admin && !configReadOnly;
  const qc = useQueryClient();
  const { toast } = useFeedback();
  const list = useQuery({ queryKey: ["filters"], queryFn: () => unwrap(api.GET("/filters")) });
  const settings = useQuery({ queryKey: ["settings"], queryFn: () => unwrap(api.GET("/settings")) });
  const [f, setF] = useState({ list: "deny" as "allow" | "deny", pattern: "", description: "" });
  const [patternError, setPatternError] = useState("");
  const add = useMutation({
    mutationFn: () => unwrap(api.POST("/filters", { body: f })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["filters"] });
      setF({ ...f, pattern: "", description: "" });
      toast("Filter angelegt");
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const del = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/filters/{id}", { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["filters"] }),
  });
  const setSettings = useMutation({
    mutationFn: (s: S["Settings"]) => unwrap(api.PUT("/settings", { body: s })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["settings"] });
      toast("Filter-Einstellungen gespeichert");
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const st = settings.data;
  return (
    <div className="space-y-6">
      <Card title="Aktivierung">
        {st && (
          <div className="flex flex-col gap-3">
            <Toggle label="Deny-Liste aktiv (gelistete MACs werden ignoriert)" checked={st.mac_filter_deny_enabled} disabled={!isAdmin} onChange={(v) => setSettings.mutate({ ...st, mac_filter_deny_enabled: v })} />
            <Toggle label="Allow-Liste aktiv (nur gelistete MACs werden bedient)" checked={st.mac_filter_allow_enabled} disabled={!isAdmin} onChange={(v) => setSettings.mutate({ ...st, mac_filter_allow_enabled: v })} />
            {!isAdmin && <p className="text-xs text-slate-500">Nur Administratoren können die Listen aktivieren.</p>}
          </div>
        )}
      </Card>
      <Card title="Einträge">
        <Table head={["Liste", "MAC / Muster", "Beschreibung", ""]} empty={list.data?.length === 0}>
          {list.data?.map((x) => (
            <tr key={x.id}>
              <Td>{x.list === "deny" ? <Badge color="red">Deny</Badge> : <Badge color="green">Allow</Badge>}</Td>
              <Td mono>{x.pattern}</Td>
              <Td>{x.description}</Td>
              <Td className="text-right">
                {canWrite && (
                  <Button size="sm" variant="ghost" onClick={() => del.mutate(x.id)}>
                    Entfernen
                  </Button>
                )}
              </Td>
            </tr>
          ))}
        </Table>
        {canWrite && (
          <form
            className="mt-4 grid gap-3 sm:grid-cols-[1fr_2fr_2fr_auto] sm:items-end"
            onSubmit={(e) => {
              e.preventDefault();
              if (!validMACPattern(f.pattern)) return setPatternError("MAC oder Muster wie 00:15:5d:*");
              setPatternError("");
              add.mutate();
            }}
          >
            <Select label="Liste" value={f.list} onChange={(e) => setF({ ...f, list: e.target.value as "allow" | "deny" })}>
              <option value="deny">Deny</option>
              <option value="allow">Allow</option>
            </Select>
            <Input label="MAC / Muster" value={f.pattern} onChange={(e) => setF({ ...f, pattern: e.target.value })} error={patternError} />
            <Input label="Beschreibung" value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} />
            <Button type="submit" variant="primary">
              Hinzufügen
            </Button>
          </form>
        )}
      </Card>
    </div>
  );
}

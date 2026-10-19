import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { OptionsEditor } from "../components/OptionsEditor";
import { Alert, Badge, Button, Card, Input, Modal, PageHeader, Select, Spinner, Table, Tabs, Td, Toggle, useFeedback } from "../components/ui";
import { messages } from "../i18n";
import { c } from "../i18n/common";
import { formatDuration } from "../lib/format";
import { hasErrors, validateRange, validMACPattern, type Errors } from "../lib/validate";
import { useScopes } from "./Scopes";

const m = messages({
  en: {
    title: "Policies & filters",
    subtitle: "Conditional options and address ranges, classes, MAC filters",
    serverPolicies: "Server policies",
    scopePolicies: "Scope policies",
    classesTab: "Vendor/user classes",
    filtersTab: "MAC filters",
    scopePoliciesInfo: "Scope policies are managed in the detail view of each scope. Server policies apply to all scopes.",
    fieldMac: "MAC address",
    fieldClientId: "Client ID",
    fieldHostname: "Hostname",
    fieldVendorClass: "Vendor class (opt. 60)",
    fieldUserClass: "User class (opt. 77)",
    fieldClass: "Defined class",
    fieldCircuitId: "Relay circuit ID (82.1)",
    fieldRemoteId: "Relay remote ID (82.2)",
    fieldSubscriberId: "Relay subscriber ID (82.6)",
    policyDeleted: "Policy deleted",
    policySaved: "Policy saved",
    newPolicy: "New policy",
    order: "Order",
    name: "Name",
    conditions: "Conditions",
    ranges: "Ranges",
    leaseDuration: "Lease duration",
    status: "Status",
    and: "AND",
    or: "OR",
    inherited: "inherited",
    off: "off",
    confirmDeletePolicy: "Delete policy “{name}”?",
    nameRequired: "Name is required",
    oneCondition: "At least one condition",
    valueRequired: "Value required",
    macPattern: "MAC or pattern like 00:15:5d:*",
    leaseMin: "0 or at least 10 seconds",
    policyTitle: "Policy “{name}”",
    orderHint: "0 = at the end",
    leaseSeconds: "Lease duration (s)",
    leaseHint: "empty = from scope",
    description: "Description",
    policyActive: "Policy active",
    combination: "Combination",
    matchAll: "all must match (AND)",
    matchAny: "one must match (OR)",
    field: "Field",
    operator: "Operator",
    is: "is",
    isNot: "is not",
    class: "Class",
    classOption: "– Class –",
    value: "Value",
    valueHint: "Value, wildcards * and ?, 0x… for hex",
    addCondition: "Add condition",
    ownRanges: "Own address ranges (matching clients only)",
    rangeFrom: "Range from",
    rangeTo: "Range to",
    addRange: "Add range",
    policyOptions: "Options of this policy",
    optionsAfterSave: "Options can be set after saving for the first time.",
    classCreated: "Class created",
    classes: "Classes",
    type: "Type",
    pattern: "Pattern",
    vendorBadge: "Vendor (60)",
    userBadge: "User (77)",
    confirmDeleteClass: "Delete class “{name}”?",
    vendorClass: "Vendor class",
    userClass: "User class",
    filterCreated: "Filter created",
    filterSettingsSaved: "Filter settings saved",
    activation: "Activation",
    denyActive: "Deny list active (listed MACs are ignored)",
    allowActive: "Allow list active (only listed MACs are served)",
    adminsOnly: "Only administrators can activate the lists.",
    entries: "Entries",
    list: "List",
    macOrPattern: "MAC / pattern",
    deny: "Deny",
    allow: "Allow",
  },
  de: {
    title: "Policies & Filter",
    subtitle: "Bedingte Optionen und Adressbereiche, Klassen, MAC-Filter",
    serverPolicies: "Server-Policies",
    scopePolicies: "Scope-Policies",
    classesTab: "Vendor-/User-Klassen",
    filtersTab: "MAC-Filter",
    scopePoliciesInfo: "Scope-Policies werden in der Detailansicht des jeweiligen Scopes gepflegt. Server-Policies gelten für alle Scopes.",
    fieldMac: "MAC-Adresse",
    fieldClientId: "Client-ID",
    fieldHostname: "Hostname",
    fieldVendorClass: "Vendor-Class (Opt. 60)",
    fieldUserClass: "User-Class (Opt. 77)",
    fieldClass: "Definierte Klasse",
    fieldCircuitId: "Relay Circuit-ID (82.1)",
    fieldRemoteId: "Relay Remote-ID (82.2)",
    fieldSubscriberId: "Relay Subscriber-ID (82.6)",
    policyDeleted: "Policy gelöscht",
    policySaved: "Policy gespeichert",
    newPolicy: "Neue Policy",
    order: "Reihenfolge",
    name: "Name",
    conditions: "Bedingungen",
    ranges: "Bereiche",
    leaseDuration: "Lease-Dauer",
    status: "Status",
    and: "UND",
    or: "ODER",
    inherited: "geerbt",
    off: "aus",
    confirmDeletePolicy: "Policy „{name}“ löschen?",
    nameRequired: "Name ist erforderlich",
    oneCondition: "Mindestens eine Bedingung",
    valueRequired: "Wert erforderlich",
    macPattern: "MAC oder Muster wie 00:15:5d:*",
    leaseMin: "0 oder mindestens 10 Sekunden",
    policyTitle: "Policy „{name}“",
    orderHint: "0 = ans Ende",
    leaseSeconds: "Lease-Dauer (s)",
    leaseHint: "leer = vom Scope",
    description: "Beschreibung",
    policyActive: "Policy aktiv",
    combination: "Verknüpfung",
    matchAll: "alle müssen zutreffen (UND)",
    matchAny: "eine muss zutreffen (ODER)",
    field: "Feld",
    operator: "Operator",
    is: "ist",
    isNot: "ist nicht",
    class: "Klasse",
    classOption: "– Klasse –",
    value: "Wert",
    valueHint: "Wert, Wildcards * und ?, 0x… für Hex",
    addCondition: "Bedingung hinzufügen",
    ownRanges: "Eigene Adressbereiche (nur passende Clients)",
    rangeFrom: "Bereich von",
    rangeTo: "Bereich bis",
    addRange: "Bereich hinzufügen",
    policyOptions: "Optionen dieser Policy",
    optionsAfterSave: "Optionen können nach dem ersten Speichern festgelegt werden.",
    classCreated: "Klasse angelegt",
    classes: "Klassen",
    type: "Typ",
    pattern: "Muster",
    vendorBadge: "Vendor (60)",
    userBadge: "User (77)",
    confirmDeleteClass: "Klasse „{name}“ löschen?",
    vendorClass: "Vendor-Class",
    userClass: "User-Class",
    filterCreated: "Filter angelegt",
    filterSettingsSaved: "Filter-Einstellungen gespeichert",
    activation: "Aktivierung",
    denyActive: "Deny-Liste aktiv (gelistete MACs werden ignoriert)",
    allowActive: "Allow-Liste aktiv (nur gelistete MACs werden bedient)",
    adminsOnly: "Nur Administratoren können die Listen aktivieren.",
    entries: "Einträge",
    list: "Liste",
    macOrPattern: "MAC / Muster",
    deny: "Deny",
    allow: "Allow",
  },
});

type Tab = "policies" | "classes" | "filters";

export function PoliciesPage() {
  const [tab, setTab] = useState<Tab>("policies");
  return (
    <>
      <PageHeader title={m("title")} subtitle={m("subtitle")} />
      <Tabs<Tab>
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "policies", label: m("serverPolicies") },
          { id: "classes", label: m("classesTab") },
          { id: "filters", label: m("filtersTab") },
        ]}
      />
      {tab === "policies" && (
        <>
          <Alert kind="info">{m("scopePoliciesInfo")}</Alert>
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

/** Labels of the condition fields, in the active language. */
const fieldLabels = (): Record<S["ConditionField"], string> => ({
  mac: m("fieldMac"),
  client_id: m("fieldClientId"),
  hostname: m("fieldHostname"),
  vendor_class: m("fieldVendorClass"),
  user_class: m("fieldUserClass"),
  class: m("fieldClass"),
  relay_circuit_id: m("fieldCircuitId"),
  relay_remote_id: m("fieldRemoteId"),
  relay_subscriber_id: m("fieldSubscriberId"),
});

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
      toast(m("policyDeleted"));
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  if (list.isLoading) return <Spinner />;
  const labels = fieldLabels();
  return (
    <Card
      title={scopeId ? m("scopePolicies") : m("serverPolicies")}
      actions={
        canWrite && (
          <Button variant="primary" size="sm" onClick={() => setEdit("new")}>
            {m("newPolicy")}
          </Button>
        )
      }
    >
      <Table head={[m("order"), m("name"), m("conditions"), m("ranges"), m("leaseDuration"), m("status"), ""]} empty={list.data?.length === 0}>
        {list.data?.map((p) => (
          <tr key={p.id}>
            <Td className="tabular-nums">{p.order}</Td>
            <Td className="font-medium">{p.name}</Td>
            <Td className="max-w-sm truncate whitespace-normal">
              {p.conditions.map((c, i) => (
                <span key={i} className="mr-1 text-xs">
                  {i > 0 && <b className="mr-1">{p.operator === "and" ? m("and") : m("or")}</b>}
                  {labels[c.field]} {c.operator === "eq" ? "=" : "≠"} <code>{c.value}</code>
                </span>
              ))}
            </Td>
            <Td mono>{p.ranges.map((r) => `${r.start}–${r.end}`).join(", ") || "–"}</Td>
            <Td>{p.lease_time == null ? m("inherited") : formatDuration(p.lease_time)}</Td>
            <Td>{p.enabled ? <Badge color="green">{c("active")}</Badge> : <Badge>{m("off")}</Badge>}</Td>
            <Td className="text-right">
              <div className="flex justify-end gap-1">
                <Button size="sm" variant="ghost" onClick={() => setEdit(p)}>
                  {canWrite ? c("edit") : c("details")}
                </Button>
                {canWrite && (
                  <Button size="sm" variant="ghost" onClick={async () => (await confirm(m("confirmDeletePolicy", { name: p.name }), { danger: true })) && del.mutate(p.id)}>
                    {c("delete")}
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
      toast(m("policySaved"));
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
    if (!p.name.trim()) e.name = m("nameRequired");
    if (p.conditions.length === 0) e.conditions = m("oneCondition");
    p.conditions.forEach((c, i) => {
      if (!c.value.trim()) e[`conditions[${i}].value`] = m("valueRequired");
      else if (c.field === "mac" && !validMACPattern(c.value)) e[`conditions[${i}].value`] = m("macPattern");
    });
    p.ranges.forEach((r, i) => {
      const re = validateRange(r.start, r.end, scope ? { start: scope.start, end: scope.end } : undefined);
      if (hasErrors(re)) e[`ranges[${i}]`] = Object.values(re)[0];
    });
    if (p.lease !== "" && (!/^\d+$/.test(p.lease) || (Number(p.lease) !== 0 && Number(p.lease) < 10))) e.lease_time = m("leaseMin");
    setErrors(e);
    return !hasErrors(e);
  };

  const setCond = (i: number, c: Partial<S["Condition"]>) => setP({ ...p, conditions: p.conditions.map((x, j) => (j === i ? { ...x, ...c } : x)) });

  return (
    <Modal
      title={saved ? m("policyTitle", { name: saved.name }) : m("newPolicy")}
      onClose={onClose}
      wide
      footer={
        canWrite && (
          <Button variant="primary" onClick={() => validate() && save.mutate()} disabled={save.isPending}>
            {c("save")}
          </Button>
        )
      }
    >
      {error && <Alert>{error}</Alert>}
      <div className="grid gap-4 sm:grid-cols-3">
        <Input label={m("name")} value={p.name} onChange={(e) => setP({ ...p, name: e.target.value })} error={errors.name} />
        <Input label={m("order")} type="number" min={0} value={p.order} onChange={(e) => setP({ ...p, order: Number(e.target.value) })} hint={m("orderHint")} />
        <Input label={m("leaseSeconds")} value={p.lease} onChange={(e) => setP({ ...p, lease: e.target.value })} error={errors.lease_time} hint={m("leaseHint")} />
      </div>
      <Input label={m("description")} value={p.description} onChange={(e) => setP({ ...p, description: e.target.value })} />
      <Toggle label={m("policyActive")} checked={p.enabled} onChange={(v) => setP({ ...p, enabled: v })} />

      <fieldset className="space-y-2">
        <legend className="mb-1 flex items-center gap-3 text-sm font-medium">
          {m("conditions")}
          <select
            aria-label={m("combination")}
            value={p.operator}
            onChange={(e) => setP({ ...p, operator: e.target.value as "and" | "or" })}
            className="rounded-md border border-slate-300 bg-white px-2 py-1 text-xs dark:border-slate-700 dark:bg-slate-900"
          >
            <option value="and">{m("matchAll")}</option>
            <option value="or">{m("matchAny")}</option>
          </select>
        </legend>
        {errors.conditions && <p className="text-xs text-red-600">{errors.conditions}</p>}
        {p.conditions.map((cond, i) => (
          <div key={i} className="grid gap-2 sm:grid-cols-[1.4fr_0.7fr_2fr_auto]">
            <select
              aria-label={m("field")}
              value={cond.field}
              onChange={(e) => setCond(i, { field: e.target.value as S["ConditionField"] })}
              className="rounded-md border border-slate-300 bg-white px-2 py-2 text-sm dark:border-slate-700 dark:bg-slate-900"
            >
              {Object.entries(fieldLabels()).map(([k, v]) => (
                <option key={k} value={k}>
                  {v}
                </option>
              ))}
            </select>
            <select
              aria-label={m("operator")}
              value={cond.operator}
              onChange={(e) => setCond(i, { operator: e.target.value as "eq" | "ne" })}
              className="rounded-md border border-slate-300 bg-white px-2 py-2 text-sm dark:border-slate-700 dark:bg-slate-900"
            >
              <option value="eq">{m("is")}</option>
              <option value="ne">{m("isNot")}</option>
            </select>
            {cond.field === "class" ? (
              <select
                aria-label={m("class")}
                value={cond.value}
                onChange={(e) => setCond(i, { value: e.target.value })}
                className="rounded-md border border-slate-300 bg-white px-2 py-2 text-sm dark:border-slate-700 dark:bg-slate-900"
              >
                <option value="">{m("classOption")}</option>
                {classes.data?.map((k) => (
                  <option key={k.id} value={k.name}>
                    {k.name} ({k.type})
                  </option>
                ))}
              </select>
            ) : (
              <div>
                <input
                  aria-label={m("value")}
                  value={cond.value}
                  placeholder={m("valueHint")}
                  onChange={(e) => setCond(i, { value: e.target.value })}
                  className={`w-full rounded-md border bg-white px-2 py-2 text-sm dark:bg-slate-900 ${errors[`conditions[${i}].value`] ? "border-red-500" : "border-slate-300 dark:border-slate-700"}`}
                />
                {errors[`conditions[${i}].value`] && <p className="text-xs text-red-600">{errors[`conditions[${i}].value`]}</p>}
              </div>
            )}
            <Button size="sm" variant="ghost" onClick={() => setP({ ...p, conditions: p.conditions.filter((_, j) => j !== i) })}>
              {c("remove")}
            </Button>
          </div>
        ))}
        <Button size="sm" onClick={() => setP({ ...p, conditions: [...p.conditions, { field: "mac", operator: "eq", value: "" }] })}>
          {m("addCondition")}
        </Button>
      </fieldset>

      {scopeId > 0 && (
        <fieldset className="space-y-2">
          <legend className="mb-1 text-sm font-medium">{m("ownRanges")}</legend>
          {p.ranges.map((r, i) => (
            <div key={i} className="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
              <input
                aria-label={m("rangeFrom")}
                value={r.start}
                onChange={(e) => setP({ ...p, ranges: p.ranges.map((x, j) => (j === i ? { ...x, start: e.target.value } : x)) })}
                className="rounded-md border border-slate-300 bg-white px-2 py-2 font-mono text-sm dark:border-slate-700 dark:bg-slate-900"
              />
              <input
                aria-label={m("rangeTo")}
                value={r.end}
                onChange={(e) => setP({ ...p, ranges: p.ranges.map((x, j) => (j === i ? { ...x, end: e.target.value } : x)) })}
                className="rounded-md border border-slate-300 bg-white px-2 py-2 font-mono text-sm dark:border-slate-700 dark:bg-slate-900"
              />
              <Button size="sm" variant="ghost" onClick={() => setP({ ...p, ranges: p.ranges.filter((_, j) => j !== i) })}>
                {c("remove")}
              </Button>
              {errors[`ranges[${i}]`] && <p className="text-xs text-red-600 sm:col-span-3">{errors[`ranges[${i}]`]}</p>}
            </div>
          ))}
          <Button size="sm" onClick={() => setP({ ...p, ranges: [...p.ranges, { start: "", end: "" }] })}>
            {m("addRange")}
          </Button>
        </fieldset>
      )}

      {saved ? (
        <Card title={m("policyOptions")}>
          <OptionsEditor owner="policy" ownerId={saved.id} />
        </Card>
      ) : (
        <p className="text-xs text-slate-500">{m("optionsAfterSave")}</p>
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
      toast(m("classCreated"));
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const del = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/classes/{id}", { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["classes"] }),
    onError: (e) => toast(errorMessage(e), "error"),
  });
  return (
    <Card title={m("classes")}>
      <Table head={[m("name"), m("type"), m("pattern"), m("description"), ""]} empty={list.data?.length === 0}>
        {list.data?.map((k) => (
          <tr key={k.id}>
            <Td className="font-medium">{k.name}</Td>
            <Td>
              <Badge color={k.type === "vendor" ? "blue" : "teal"}>{k.type === "vendor" ? m("vendorBadge") : m("userBadge")}</Badge>
            </Td>
            <Td mono>{k.data}</Td>
            <Td>{k.description}</Td>
            <Td className="text-right">
              {canWrite && (
                <Button size="sm" variant="ghost" onClick={async () => (await confirm(m("confirmDeleteClass", { name: k.name }), { danger: true })) && del.mutate(k.id)}>
                  {c("delete")}
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
          <Input label={m("name")} value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} required />
          <Select label={m("type")} value={f.type} onChange={(e) => setF({ ...f, type: e.target.value as "vendor" | "user" })}>
            <option value="vendor">{m("vendorClass")}</option>
            <option value="user">{m("userClass")}</option>
          </Select>
          <Input label={m("pattern")} value={f.data} onChange={(e) => setF({ ...f, data: e.target.value })} placeholder="PXEClient*" required />
          <Input label={m("description")} value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} />
          <Button type="submit" variant="primary">
            {c("create")}
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
      toast(m("filterCreated"));
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
      toast(m("filterSettingsSaved"));
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const st = settings.data;
  return (
    <div className="space-y-6">
      <Card title={m("activation")}>
        {st && (
          <div className="flex flex-col gap-3">
            <Toggle label={m("denyActive")} checked={st.mac_filter_deny_enabled} disabled={!isAdmin} onChange={(v) => setSettings.mutate({ ...st, mac_filter_deny_enabled: v })} />
            <Toggle label={m("allowActive")} checked={st.mac_filter_allow_enabled} disabled={!isAdmin} onChange={(v) => setSettings.mutate({ ...st, mac_filter_allow_enabled: v })} />
            {!isAdmin && <p className="text-xs text-slate-500">{m("adminsOnly")}</p>}
          </div>
        )}
      </Card>
      <Card title={m("entries")}>
        <Table head={[m("list"), m("macOrPattern"), m("description"), ""]} empty={list.data?.length === 0}>
          {list.data?.map((x) => (
            <tr key={x.id}>
              <Td>{x.list === "deny" ? <Badge color="red">{m("deny")}</Badge> : <Badge color="green">{m("allow")}</Badge>}</Td>
              <Td mono>{x.pattern}</Td>
              <Td>{x.description}</Td>
              <Td className="text-right">
                {canWrite && (
                  <Button size="sm" variant="ghost" onClick={() => del.mutate(x.id)}>
                    {c("remove")}
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
              if (!validMACPattern(f.pattern)) return setPatternError(m("macPattern"));
              setPatternError("");
              add.mutate();
            }}
          >
            <Select label={m("list")} value={f.list} onChange={(e) => setF({ ...f, list: e.target.value as "allow" | "deny" })}>
              <option value="deny">{m("deny")}</option>
              <option value="allow">{m("allow")}</option>
            </Select>
            <Input label={m("macOrPattern")} value={f.pattern} onChange={(e) => setF({ ...f, pattern: e.target.value })} error={patternError} />
            <Input label={m("description")} value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} />
            <Button type="submit" variant="primary">
              {c("add")}
            </Button>
          </form>
        )}
      </Card>
    </div>
  );
}

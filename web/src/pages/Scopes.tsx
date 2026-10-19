import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router";
import { api, ApiError, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { Alert, Badge, Button, Input, Modal, PageHeader, Progress, Select, Spinner, Table, Td, TextArea, useFeedback } from "../components/ui";
import { messages } from "../i18n";
import { c } from "../i18n/common";
import { readJSONFile } from "../lib/download";
import { formatDuration } from "../lib/format";
import { broadcastOf, formatIPv4, hasErrors, networkOf, parsePrefix, validateRange, validateScope, type Errors } from "../lib/validate";

const m = messages({
  en: {
    hour1: "1 hour",
    hours8: "8 hours",
    day1: "1 day",
    days8: "8 days",
    title: "Scopes",
    subtitle: "IPv4 ranges that are assigned dynamically",
    imported: "Scope “{name}” imported",
    deleted: "Scope deleted",
    newScope: "New scope",
    name: "Name",
    subnet: "Subnet",
    range: "Range",
    leaseTime: "Lease duration",
    status: "Status",
    utilization: "Utilization",
    superscope: "Superscope",
    deleteConfirm: "Delete scope “{name}” including its reservations, policies, options and leases?",
    ssCreated: "Superscope created – assign scopes to it in each scope under “Settings”",
    ssDeleted: "Superscope deleted",
    superscopes: "Superscopes",
    ssInfo:
      "A superscope groups scopes that serve the same physical network (several IP networks on one segment). When a scope is exhausted, clients receive addresses from the other members.",
    scopes: "Scopes",
    ssDeleteConfirm: "Delete superscope “{name}”? The scopes are kept.",
    newSuperscope: "New superscope",
    ssPlaceholder: "e.g. Building A",
    stepBasics: "Basics",
    stepNetwork: "Network",
    stepOptions: "Options",
    stepExclusions: "Exclusions",
    stepSummary: "Summary",
    created: "Scope “{name}” created",
    createdPartly: " – the scope was created, please check its details.",
    wizardTitle: "New scope – {step} ({n}/{total})",
    createScope: "Create scope",
    steps: "Steps",
    namePlaceholder: "e.g. Office LAN",
    description: "Description",
    start: "Start address",
    end: "End address",
    suggestionHint: "leave empty to use the suggestion",
    router: "Router (option 3)",
    dns: "DNS servers (option 6)",
    domain: "Domain (option 15)",
    moreOptions: "More options can be set in the scope after it has been created.",
    exclusionInfo: "Optional: exclude an address range from assignment (e.g. for static devices).",
    exStart: "Exclusion start",
    exEnd: "Exclusion end",
    summaryOptions: "Router / DNS / domain",
    exclusion: "Exclusion",
  },
  de: {
    hour1: "1 Stunde",
    hours8: "8 Stunden",
    day1: "1 Tag",
    days8: "8 Tage",
    title: "Scopes",
    subtitle: "IPv4-Bereiche, die dynamisch vergeben werden",
    imported: "Scope „{name}“ importiert",
    deleted: "Scope gelöscht",
    newScope: "Neuer Scope",
    name: "Name",
    subnet: "Subnetz",
    range: "Bereich",
    leaseTime: "Lease-Dauer",
    status: "Status",
    utilization: "Auslastung",
    superscope: "Superscope",
    deleteConfirm: "Scope „{name}“ samt Reservierungen, Policies, Optionen und Leases löschen?",
    ssCreated: "Superscope angelegt – Scopes im Scope unter „Einstellungen“ zuordnen",
    ssDeleted: "Superscope gelöscht",
    superscopes: "Superscopes",
    ssInfo:
      "Ein Superscope fasst Scopes zusammen, die dasselbe physische Netz versorgen (mehrere IP-Netze auf einem Segment). Ist ein Scope erschöpft, erhalten Clients Adressen aus den anderen Mitgliedern.",
    scopes: "Scopes",
    ssDeleteConfirm: "Superscope „{name}“ löschen? Die Scopes bleiben erhalten.",
    newSuperscope: "Neuer Superscope",
    ssPlaceholder: "z. B. Gebäude A",
    stepBasics: "Grunddaten",
    stepNetwork: "Netz",
    stepOptions: "Optionen",
    stepExclusions: "Ausschlüsse",
    stepSummary: "Zusammenfassung",
    created: "Scope „{name}“ angelegt",
    createdPartly: " – der Scope wurde angelegt, bitte die Details prüfen.",
    wizardTitle: "Neuer Scope – {step} ({n}/{total})",
    createScope: "Scope anlegen",
    steps: "Schritte",
    namePlaceholder: "z. B. Büro-LAN",
    description: "Beschreibung",
    start: "Startadresse",
    end: "Endadresse",
    suggestionHint: "leer lassen für Vorschlag",
    router: "Router (Option 3)",
    dns: "DNS-Server (Option 6)",
    domain: "Domäne (Option 15)",
    moreOptions: "Weitere Optionen lassen sich nach dem Anlegen im Scope festlegen.",
    exclusionInfo: "Optional: einen Adressbereich von der Vergabe ausschließen (z. B. für statische Geräte).",
    exStart: "Ausschluss von",
    exEnd: "Ausschluss bis",
    summaryOptions: "Router / DNS / Domäne",
    exclusion: "Ausschluss",
  },
});

/** The lease durations offered in scope forms, with labels in the active language. */
export const leaseTimes = () => [
  { v: 3600, l: m("hour1") },
  { v: 8 * 3600, l: m("hours8") },
  { v: 86400, l: m("day1") },
  { v: 8 * 86400, l: m("days8") },
  { v: 0, l: c("unlimited") },
];

export function useScopes() {
  return useQuery({ queryKey: ["scopes"], queryFn: () => unwrap(api.GET("/scopes")) });
}

export function useSuperscopes() {
  return useQuery({ queryKey: ["superscopes"], queryFn: () => unwrap(api.GET("/superscopes")) });
}

export function ScopesPage() {
  const { canWrite } = useAuth();
  const scopes = useScopes();
  const stats = useQuery({ queryKey: ["stats"], queryFn: () => unwrap(api.GET("/stats")) });
  const [wizard, setWizard] = useState(false);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { toast, confirm } = useFeedback();
  const imp = useMutation({
    mutationFn: async (f: File) => unwrap(api.POST("/scopes/import", { body: await readJSONFile<S["ScopeBundle"]>(f) })),
    onSuccess: (sc) => {
      qc.invalidateQueries({ queryKey: ["scopes"] });
      qc.invalidateQueries({ queryKey: ["stats"] });
      toast(m("imported", { name: sc.name }));
      navigate(`/scopes/${sc.id}`);
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const del = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/scopes/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["scopes"] });
      qc.invalidateQueries({ queryKey: ["stats"] });
      toast(m("deleted"));
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const util = useMemo(() => new Map(stats.data?.scopes.map((s) => [s.scope_id, s.utilization_percent])), [stats.data]);
  const superscopes = useSuperscopes();
  const ssName = useMemo(() => new Map(superscopes.data?.map((s) => [s.id, s.name])), [superscopes.data]);

  return (
    <>
      <PageHeader
        title={m("title")}
        subtitle={m("subtitle")}
        actions={
          canWrite && (
            <div className="flex gap-2">
              <label className="inline-flex cursor-pointer items-center rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm font-medium hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:hover:bg-slate-800">
                {c("import")}
                <input
                  type="file"
                  accept="application/json,.json"
                  className="sr-only"
                  data-testid="scope-import"
                  onChange={(e) => {
                    const f = e.target.files?.[0];
                    if (f) imp.mutate(f);
                    e.target.value = "";
                  }}
                />
              </label>
              <Button variant="primary" onClick={() => setWizard(true)}>
                {m("newScope")}
              </Button>
            </div>
          )
        }
      />
      {scopes.isLoading ? (
        <Spinner />
      ) : (
        <div className="rounded-lg border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <Table head={[m("name"), m("subnet"), m("range"), m("leaseTime"), m("status"), m("utilization"), ""]} empty={scopes.data?.length === 0}>
            {scopes.data?.map((s) => (
              <tr key={s.id}>
                <Td>
                  <Link to={`/scopes/${s.id}`} className="font-medium text-teal-700 hover:underline dark:text-teal-400">
                    {s.name}
                  </Link>
                  {s.superscope_id !== 0 && (
                    <span className="ml-2">
                      <Badge color="blue">{ssName.get(s.superscope_id) ?? m("superscope")}</Badge>
                    </span>
                  )}
                  {s.bootp && (
                    <span className="ml-1">
                      <Badge>BOOTP</Badge>
                    </span>
                  )}
                </Td>
                <Td mono>{s.subnet}</Td>
                <Td mono>
                  {s.start} – {s.end}
                </Td>
                <Td>{formatDuration(s.lease_time)}</Td>
                <Td>{s.enabled ? <Badge color="green">{c("active")}</Badge> : <Badge color="amber">{c("disabled")}</Badge>}</Td>
                <Td className="w-44">
                  <Progress percent={util.get(s.id) ?? 0} />
                </Td>
                <Td className="text-right">
                  {canWrite && (
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={async () =>
                        (await confirm(m("deleteConfirm", { name: s.name }), { danger: true, confirmLabel: c("delete") })) &&
                        del.mutate(s.id)
                      }
                    >
                      {c("delete")}
                    </Button>
                  )}
                </Td>
              </tr>
            ))}
          </Table>
        </div>
      )}
      <Superscopes scopes={scopes.data ?? []} util={stats.data?.scopes ?? []} />
      {wizard && <ScopeWizard existing={scopes.data ?? []} onClose={() => setWizard(false)} />}
    </>
  );
}

/** Superscopes group scopes that serve the same physical network (multinet). */
function Superscopes({ scopes, util }: { scopes: S["Scope"][]; util: S["ScopeStats"][] }) {
  const { canWrite } = useAuth();
  const list = useSuperscopes();
  const qc = useQueryClient();
  const { toast, confirm } = useFeedback();
  const [name, setName] = useState("");
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["superscopes"] });
    qc.invalidateQueries({ queryKey: ["scopes"] });
  };
  const add = useMutation({
    mutationFn: () => unwrap(api.POST("/superscopes", { body: { name } })),
    onSuccess: () => {
      setName("");
      refresh();
      toast(m("ssCreated"));
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const del = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/superscopes/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      refresh();
      toast(m("ssDeleted"));
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const byId = new Map(scopes.map((s) => [s.id, s]));
  const stat = new Map(util.map((u) => [u.scope_id, u]));
  if (!canWrite && (list.data?.length ?? 0) === 0) return null;
  return (
    <div className="mt-8">
      <h2 className="mb-1 text-lg font-semibold">{m("superscopes")}</h2>
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">{m("ssInfo")}</p>
      <div className="rounded-lg border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <Table head={[m("name"), m("scopes"), m("utilization"), ""]} empty={list.data?.length === 0}>
          {list.data?.map((ss) => {
            const members = ss.scope_ids.map((id) => byId.get(id)).filter((s): s is S["Scope"] => !!s);
            const usable = ss.scope_ids.reduce((a, id) => a + (stat.get(id)?.usable ?? 0), 0);
            const avail = ss.scope_ids.reduce((a, id) => a + (stat.get(id)?.available ?? 0), 0);
            return (
              <tr key={ss.id}>
                <Td className="font-medium">{ss.name}</Td>
                <Td>
                  {members.length === 0 ? (
                    <span className="text-slate-500">{c("none")}</span>
                  ) : (
                    members.map((sc) => (
                      <Link key={sc.id} to={`/scopes/${sc.id}`} className="mr-3 font-mono text-xs text-teal-700 hover:underline dark:text-teal-400">
                        {sc.subnet}
                      </Link>
                    ))
                  )}
                </Td>
                <Td className="w-44">
                  <Progress percent={usable ? ((usable - avail) / usable) * 100 : 0} />
                </Td>
                <Td className="text-right">
                  {canWrite && (
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={async () =>
                        (await confirm(m("ssDeleteConfirm", { name: ss.name }), { danger: true, confirmLabel: c("delete") })) && del.mutate(ss.id)
                      }
                    >
                      {c("delete")}
                    </Button>
                  )}
                </Td>
              </tr>
            );
          })}
        </Table>
      </div>
      {canWrite && (
        <form
          className="mt-3 flex flex-wrap items-end gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (name.trim()) add.mutate();
          }}
        >
          <div className="w-64">
            <Input label={m("newSuperscope")} value={name} onChange={(e) => setName(e.target.value)} placeholder={m("ssPlaceholder")} />
          </div>
          <Button type="submit" disabled={!name.trim() || add.isPending}>
            {c("create")}
          </Button>
        </form>
      )}
    </div>
  );
}

interface WizardState {
  name: string;
  description: string;
  subnet: string;
  start: string;
  end: string;
  lease_time: number;
  router: string;
  dns: string;
  domain: string;
  exStart: string;
  exEnd: string;
}

function ScopeWizard({ existing, onClose }: { existing: { name: string; subnet: string }[]; onClose: () => void }) {
  const steps = [m("stepBasics"), m("stepNetwork"), m("stepOptions"), m("stepExclusions"), m("stepSummary")];
  const [step, setStep] = useState(0);
  const [s, setS] = useState<WizardState>({
    name: "",
    description: "",
    subnet: "",
    start: "",
    end: "",
    lease_time: 8 * 86400,
    router: "",
    dns: "",
    domain: "",
    exStart: "",
    exEnd: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { toast } = useFeedback();
  const set = (k: keyof WizardState) => (e: { target: { value: string } }) => setS({ ...s, [k]: e.target.value });

  // Suggested range for the entered subnet, shown as placeholder and only
  // applied to empty fields when continuing (no auto-fill while typing).
  const suggestion = useMemo(() => {
    const p = parsePrefix(s.subnet);
    if (!p || p.bits < 8 || p.bits > 30) return null;
    const net = networkOf(p);
    const bc = broadcastOf(p);
    const size = bc - net;
    const first = size > 20 ? net + Math.floor(size / 2.5) : net + 1;
    return { start: formatIPv4(first), end: formatIPv4(bc - 1), router: formatIPv4(net + 1) };
  }, [s.subnet]);

  const validateStep = (): boolean => {
    let e: Errors = {};
    let cur = s;
    if (step === 1 && suggestion && (!s.start.trim() || !s.end.trim())) {
      cur = { ...s, start: s.start.trim() || suggestion.start, end: s.end.trim() || suggestion.end };
      setS(cur);
    }
    const full = validateScope(cur, existing);
    if (step === 0 && full.name) e.name = full.name;
    if (step === 1) {
      e = { ...full };
      delete e.name;
    }
    if (step === 3 && (s.exStart || s.exEnd)) e = validateRange(s.exStart, s.exEnd, { start: s.start, end: s.end });
    setErrors(e);
    return !hasErrors(e);
  };

  const create = async () => {
    setBusy(true);
    setError("");
    let id = 0;
    try {
      const sc = await unwrap(
        api.POST("/scopes", {
          body: { name: s.name.trim(), description: s.description, subnet: s.subnet.trim(), start: s.start.trim(), end: s.end.trim(), lease_time: s.lease_time, enabled: true },
        }),
      );
      id = sc.id;
      const opts: [number, string][] = [
        [3, s.router],
        [6, s.dns],
        [15, s.domain],
      ];
      for (const [code, value] of opts) {
        if (value.trim()) await unwrap(api.PUT("/options/{owner}/{ownerId}/{code}", { params: { path: { owner: "scope", ownerId: id, code } }, body: { value } }));
      }
      if (s.exStart && s.exEnd) {
        await unwrap(api.POST("/scopes/{id}/exclusions", { params: { path: { id } }, body: { start: s.exStart.trim(), end: s.exEnd.trim() } }));
      }
      qc.invalidateQueries({ queryKey: ["scopes"] });
      qc.invalidateQueries({ queryKey: ["stats"] });
      toast(m("created", { name: sc.name }));
      navigate(`/scopes/${id}`);
    } catch (e) {
      if (e instanceof ApiError && Object.keys(e.fields).length) setErrors(e.fields);
      setError(errorMessage(e) + (id ? m("createdPartly") : ""));
      if (id) qc.invalidateQueries({ queryKey: ["scopes"] });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title={m("wizardTitle", { step: steps[step], n: step + 1, total: steps.length })}
      onClose={onClose}
      wide
      footer={
        <>
          {step > 0 && <Button onClick={() => setStep(step - 1)}>{c("back")}</Button>}
          {step < steps.length - 1 ? (
            <Button variant="primary" onClick={() => validateStep() && setStep(step + 1)}>
              {c("next")}
            </Button>
          ) : (
            <Button variant="primary" onClick={create} disabled={busy}>
              {m("createScope")}
            </Button>
          )}
        </>
      }
    >
      <ol className="flex flex-wrap gap-2 text-xs" aria-label={m("steps")}>
        {steps.map((t, i) => (
          <li key={t} className={i === step ? "font-semibold text-teal-700 dark:text-teal-400" : "text-slate-500"}>
            {i + 1}. {t}
          </li>
        ))}
      </ol>
      {error && <Alert>{error}</Alert>}
      {step === 0 && (
        <>
          <Input label={m("name")} name="name" value={s.name} onChange={set("name")} error={errors.name} placeholder={m("namePlaceholder")} />
          <TextArea label={m("description")} name="description" value={s.description} onChange={set("description")} rows={2} />
        </>
      )}
      {step === 1 && (
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="sm:col-span-2">
            <Input label={m("subnet")} name="subnet" value={s.subnet} onChange={set("subnet")} error={errors.subnet} placeholder="192.168.1.0/24" />
          </div>
          <Input label={m("start")} name="start" value={s.start} onChange={set("start")} error={errors.start} placeholder={suggestion?.start} hint={suggestion && !s.start ? m("suggestionHint") : undefined} />
          <Input label={m("end")} name="end" value={s.end} onChange={set("end")} error={errors.end} placeholder={suggestion?.end} />
          <Select label={m("leaseTime")} name="lease_time" value={String(s.lease_time)} onChange={(e) => setS({ ...s, lease_time: Number(e.target.value) })} error={errors.lease_time}>
            {leaseTimes().map((t) => (
              <option key={t.v} value={t.v}>
                {t.l}
              </option>
            ))}
          </Select>
        </div>
      )}
      {step === 2 && (
        <>
          <Input label={m("router")} name="router" value={s.router} onChange={set("router")} placeholder={suggestion?.router ?? "192.168.1.1"} />
          <Input label={m("dns")} name="dns" value={s.dns} onChange={set("dns")} placeholder="192.168.1.53, 9.9.9.9" />
          <Input label={m("domain")} name="domain" value={s.domain} onChange={set("domain")} placeholder="example.local" />
          <p className="text-xs text-slate-500">{m("moreOptions")}</p>
        </>
      )}
      {step === 3 && (
        <div className="grid gap-4 sm:grid-cols-2">
          <p className="text-sm text-slate-600 sm:col-span-2 dark:text-slate-400">{m("exclusionInfo")}</p>
          <Input label={m("exStart")} name="exStart" value={s.exStart} onChange={set("exStart")} error={errors.start} />
          <Input label={m("exEnd")} name="exEnd" value={s.exEnd} onChange={set("exEnd")} error={errors.end} />
        </div>
      )}
      {step === 4 && (
        <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
          <dt className="text-slate-500">{m("name")}</dt>
          <dd>{s.name}</dd>
          <dt className="text-slate-500">{m("subnet")}</dt>
          <dd className="font-mono">{s.subnet}</dd>
          <dt className="text-slate-500">{m("range")}</dt>
          <dd className="font-mono">
            {s.start} – {s.end}
          </dd>
          <dt className="text-slate-500">{m("leaseTime")}</dt>
          <dd>{formatDuration(s.lease_time)}</dd>
          <dt className="text-slate-500">{m("summaryOptions")}</dt>
          <dd className="font-mono">{[s.router, s.dns, s.domain].map((v) => v || "–").join(" / ")}</dd>
          <dt className="text-slate-500">{m("exclusion")}</dt>
          <dd className="font-mono">{s.exStart ? `${s.exStart} – ${s.exEnd}` : "–"}</dd>
        </dl>
      )}
    </Modal>
  );
}

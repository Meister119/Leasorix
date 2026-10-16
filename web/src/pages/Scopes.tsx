import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router";
import { api, ApiError, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { Alert, Badge, Button, Input, Modal, PageHeader, Progress, Select, Spinner, Table, Td, TextArea, useFeedback } from "../components/ui";
import { readJSONFile } from "../lib/download";
import { formatDuration } from "../lib/format";
import { broadcastOf, formatIPv4, hasErrors, networkOf, parsePrefix, validateRange, validateScope, type Errors } from "../lib/validate";

export const leaseTimes = [
  { v: 3600, l: "1 Stunde" },
  { v: 8 * 3600, l: "8 Stunden" },
  { v: 86400, l: "1 Tag" },
  { v: 8 * 86400, l: "8 Tage" },
  { v: 0, l: "unbegrenzt" },
];

export function useScopes() {
  return useQuery({ queryKey: ["scopes"], queryFn: () => unwrap(api.GET("/scopes")) });
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
      toast(`Scope „${sc.name}“ importiert`);
      navigate(`/scopes/${sc.id}`);
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const del = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/scopes/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["scopes"] });
      qc.invalidateQueries({ queryKey: ["stats"] });
      toast("Scope gelöscht");
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const util = useMemo(() => new Map(stats.data?.scopes.map((s) => [s.scope_id, s.utilization_percent])), [stats.data]);

  return (
    <>
      <PageHeader
        title="Scopes"
        subtitle="IPv4-Bereiche, die dynamisch vergeben werden"
        actions={
          canWrite && (
            <div className="flex gap-2">
              <label className="inline-flex cursor-pointer items-center rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm font-medium hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:hover:bg-slate-800">
                Importieren…
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
                Neuer Scope
              </Button>
            </div>
          )
        }
      />
      {scopes.isLoading ? (
        <Spinner />
      ) : (
        <div className="rounded-lg border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <Table head={["Name", "Subnetz", "Bereich", "Lease-Dauer", "Status", "Auslastung", ""]} empty={scopes.data?.length === 0}>
            {scopes.data?.map((s) => (
              <tr key={s.id}>
                <Td>
                  <Link to={`/scopes/${s.id}`} className="font-medium text-teal-700 hover:underline dark:text-teal-400">
                    {s.name}
                  </Link>
                </Td>
                <Td mono>{s.subnet}</Td>
                <Td mono>
                  {s.start} – {s.end}
                </Td>
                <Td>{formatDuration(s.lease_time)}</Td>
                <Td>{s.enabled ? <Badge color="green">aktiv</Badge> : <Badge color="amber">deaktiviert</Badge>}</Td>
                <Td className="w-44">
                  <Progress percent={util.get(s.id) ?? 0} />
                </Td>
                <Td className="text-right">
                  {canWrite && (
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={async () =>
                        (await confirm(`Scope „${s.name}“ samt Reservierungen, Policies, Optionen und Leases löschen?`, { danger: true, confirmLabel: "Löschen" })) &&
                        del.mutate(s.id)
                      }
                    >
                      Löschen
                    </Button>
                  )}
                </Td>
              </tr>
            ))}
          </Table>
        </div>
      )}
      {wizard && <ScopeWizard existing={scopes.data ?? []} onClose={() => setWizard(false)} />}
    </>
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

const steps = ["Grunddaten", "Netz", "Optionen", "Ausschlüsse", "Zusammenfassung"];

function ScopeWizard({ existing, onClose }: { existing: { name: string; subnet: string }[]; onClose: () => void }) {
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
      toast(`Scope „${sc.name}“ angelegt`);
      navigate(`/scopes/${id}`);
    } catch (e) {
      if (e instanceof ApiError && Object.keys(e.fields).length) setErrors(e.fields);
      setError(errorMessage(e) + (id ? " – der Scope wurde angelegt, bitte die Details prüfen." : ""));
      if (id) qc.invalidateQueries({ queryKey: ["scopes"] });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title={`Neuer Scope – ${steps[step]} (${step + 1}/${steps.length})`}
      onClose={onClose}
      wide
      footer={
        <>
          {step > 0 && <Button onClick={() => setStep(step - 1)}>Zurück</Button>}
          {step < steps.length - 1 ? (
            <Button variant="primary" onClick={() => validateStep() && setStep(step + 1)}>
              Weiter
            </Button>
          ) : (
            <Button variant="primary" onClick={create} disabled={busy}>
              Scope anlegen
            </Button>
          )}
        </>
      }
    >
      <ol className="flex flex-wrap gap-2 text-xs" aria-label="Schritte">
        {steps.map((t, i) => (
          <li key={t} className={i === step ? "font-semibold text-teal-700 dark:text-teal-400" : "text-slate-500"}>
            {i + 1}. {t}
          </li>
        ))}
      </ol>
      {error && <Alert>{error}</Alert>}
      {step === 0 && (
        <>
          <Input label="Name" name="name" value={s.name} onChange={set("name")} error={errors.name} placeholder="z. B. Büro-LAN" />
          <TextArea label="Beschreibung" name="description" value={s.description} onChange={set("description")} rows={2} />
        </>
      )}
      {step === 1 && (
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="sm:col-span-2">
            <Input label="Subnetz" name="subnet" value={s.subnet} onChange={set("subnet")} error={errors.subnet} placeholder="192.168.1.0/24" />
          </div>
          <Input label="Startadresse" name="start" value={s.start} onChange={set("start")} error={errors.start} placeholder={suggestion?.start} hint={suggestion && !s.start ? "leer lassen für Vorschlag" : undefined} />
          <Input label="Endadresse" name="end" value={s.end} onChange={set("end")} error={errors.end} placeholder={suggestion?.end} />
          <Select label="Lease-Dauer" name="lease_time" value={String(s.lease_time)} onChange={(e) => setS({ ...s, lease_time: Number(e.target.value) })} error={errors.lease_time}>
            {leaseTimes.map((t) => (
              <option key={t.v} value={t.v}>
                {t.l}
              </option>
            ))}
          </Select>
        </div>
      )}
      {step === 2 && (
        <>
          <Input label="Router (Option 3)" name="router" value={s.router} onChange={set("router")} placeholder={suggestion?.router ?? "192.168.1.1"} />
          <Input label="DNS-Server (Option 6)" name="dns" value={s.dns} onChange={set("dns")} placeholder="192.168.1.53, 9.9.9.9" />
          <Input label="Domäne (Option 15)" name="domain" value={s.domain} onChange={set("domain")} placeholder="example.local" />
          <p className="text-xs text-slate-500">Weitere Optionen lassen sich nach dem Anlegen im Scope festlegen.</p>
        </>
      )}
      {step === 3 && (
        <div className="grid gap-4 sm:grid-cols-2">
          <p className="text-sm text-slate-600 sm:col-span-2 dark:text-slate-400">Optional: einen Adressbereich von der Vergabe ausschließen (z. B. für statische Geräte).</p>
          <Input label="Ausschluss von" name="exStart" value={s.exStart} onChange={set("exStart")} error={errors.start} />
          <Input label="Ausschluss bis" name="exEnd" value={s.exEnd} onChange={set("exEnd")} error={errors.end} />
        </div>
      )}
      {step === 4 && (
        <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
          <dt className="text-slate-500">Name</dt>
          <dd>{s.name}</dd>
          <dt className="text-slate-500">Subnetz</dt>
          <dd className="font-mono">{s.subnet}</dd>
          <dt className="text-slate-500">Bereich</dt>
          <dd className="font-mono">
            {s.start} – {s.end}
          </dd>
          <dt className="text-slate-500">Lease-Dauer</dt>
          <dd>{formatDuration(s.lease_time)}</dd>
          <dt className="text-slate-500">Router / DNS / Domäne</dt>
          <dd className="font-mono">{[s.router, s.dns, s.domain].map((v) => v || "–").join(" / ")}</dd>
          <dt className="text-slate-500">Ausschluss</dt>
          <dd className="font-mono">{s.exStart ? `${s.exStart} – ${s.exEnd}` : "–"}</dd>
        </dl>
      )}
    </Modal>
  );
}

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { OptionsEditor } from "../components/OptionsEditor";
import { Alert, Button, Card, Input, Modal, PageHeader, Select, Spinner, Table, Td, TextArea, useFeedback } from "../components/ui";
import { messages } from "../i18n";
import { c } from "../i18n/common";
import { hasErrors, validateReservation, type Errors } from "../lib/validate";
import { useScopes } from "./Scopes";

const m = messages({
  en: {
    title: "Reservations",
    subtitle: "Fixed addresses for specific clients (MAC or client ID)",
    csvImport: "CSV import",
    scope: "Scope",
    allScopes: "All scopes",
    created: "Reservation created",
    deleted: "Reservation deleted",
    saved: "Reservation saved",
    selectScope: "Select a scope",
    clientId: "Client ID",
    hostname: "Hostname",
    description: "Description",
    scopeOption: "– Scope –",
    confirmDelete: "Delete reservation {ip}?",
    editTitle: "Reservation {ip}",
    ipAddress: "IP address",
    macAddress: "MAC address",
    clientIdHex: "Client ID (hex)",
    options: "Reservation options (highest priority)",
    importTitle: "Import reservations (CSV)",
    import: "Import",
    columns: "Columns:",
    nameOrId: "(name or ID)",
    andOr: "and/or",
    optional: "optionally",
    csvRules: "Separator: comma or semicolon. Either everything or nothing is imported.",
    csvFile: "CSV file",
    csvContent: "CSV content",
    imported: "{n} reservations imported.",
    nothingImported: "Nothing imported – please fix:",
    lineError: "Line {line}: {message}",
  },
  de: {
    title: "Reservierungen",
    subtitle: "Feste Adressen für bestimmte Clients (MAC oder Client-ID)",
    csvImport: "CSV-Import",
    scope: "Scope",
    allScopes: "Alle Scopes",
    created: "Reservierung angelegt",
    deleted: "Reservierung gelöscht",
    saved: "Reservierung gespeichert",
    selectScope: "Scope wählen",
    clientId: "Client-ID",
    hostname: "Hostname",
    description: "Beschreibung",
    scopeOption: "– Scope –",
    confirmDelete: "Reservierung {ip} löschen?",
    editTitle: "Reservierung {ip}",
    ipAddress: "IP-Adresse",
    macAddress: "MAC-Adresse",
    clientIdHex: "Client-ID (hex)",
    options: "Reservierungs-Optionen (höchste Priorität)",
    importTitle: "Reservierungen importieren (CSV)",
    import: "Importieren",
    columns: "Spalten:",
    nameOrId: "(Name oder ID)",
    andOr: "und/oder",
    optional: "optional",
    csvRules: "Trennzeichen Komma oder Semikolon. Es wird alles oder nichts importiert.",
    csvFile: "CSV-Datei",
    csvContent: "CSV-Inhalt",
    imported: "{n} Reservierungen importiert.",
    nothingImported: "Nichts importiert – bitte korrigieren:",
    lineError: "Zeile {line}: {message}",
  },
});

export function ReservationsPage() {
  const scopes = useScopes();
  const [scopeId, setScopeId] = useState(0);
  const [importOpen, setImportOpen] = useState(false);
  const { canWrite } = useAuth();
  return (
    <>
      <PageHeader
        title={m("title")}
        subtitle={m("subtitle")}
        actions={canWrite && <Button onClick={() => setImportOpen(true)}>{m("csvImport")}</Button>}
      />
      <div className="mb-4 max-w-xs">
        <Select label={m("scope")} value={String(scopeId)} onChange={(e) => setScopeId(Number(e.target.value))}>
          <option value="0">{m("allScopes")}</option>
          {scopes.data?.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name} ({s.subnet})
            </option>
          ))}
        </Select>
      </div>
      <ReservationsTable scopeId={scopeId} />
      {importOpen && <ImportDialog onClose={() => setImportOpen(false)} />}
    </>
  );
}

const empty = { ip: "", mac: "", client_id: "", hostname: "", description: "" };

/** Reservation table with inline creation; scopeId 0 = all scopes. */
export function ReservationsTable({ scopeId }: { scopeId: number }) {
  const { canWrite } = useAuth();
  const scopes = useScopes();
  const qc = useQueryClient();
  const { toast, confirm } = useFeedback();
  const key = ["reservations", scopeId];
  const list = useQuery({
    queryKey: key,
    queryFn: () => unwrap(api.GET("/reservations", { params: { query: scopeId ? { scope_id: scopeId } : {} } })),
  });
  const [row, setRow] = useState({ ...empty, scope_id: scopeId });
  const [errors, setErrors] = useState<Errors>({});
  const [edit, setEdit] = useState<S["Reservation"] | null>(null);
  const scopeById = new Map(scopes.data?.map((s) => [s.id, s]));

  const refresh = () => qc.invalidateQueries({ queryKey: ["reservations"] });
  const add = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/reservations", {
          body: { scope_id: row.scope_id || scopeId, ip: row.ip.trim(), mac: row.mac.trim(), client_id: row.client_id.trim(), hostname: row.hostname.trim(), description: row.description },
        }),
      ),
    onSuccess: () => {
      refresh();
      setRow({ ...empty, scope_id: row.scope_id });
      toast(m("created"));
    },
    onError: (e) => {
      if (e instanceof ApiError) setErrors(e.fields);
      toast(errorMessage(e), "error");
    },
  });
  const del = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/reservations/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      refresh();
      toast(m("deleted"));
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });

  const submit = () => {
    const sid = row.scope_id || scopeId;
    const e = validateReservation(row, scopeById.get(sid)?.subnet);
    if (!sid) e.scope_id = m("selectScope");
    setErrors(e);
    if (!hasErrors(e)) add.mutate();
  };

  if (list.isLoading) return <Spinner />;
  return (
    <div className="rounded-lg border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
      <Table head={["IP", "MAC", m("clientId"), m("hostname"), m("scope"), m("description"), ""]} empty={list.data?.length === 0 && !canWrite}>
        {canWrite && (
          <tr className="bg-slate-50 dark:bg-slate-950/40">
            <Td>
              <InlineInput label="IP" value={row.ip} onChange={(v) => setRow({ ...row, ip: v })} error={errors.ip} />
            </Td>
            <Td>
              <InlineInput label="MAC" value={row.mac} onChange={(v) => setRow({ ...row, mac: v })} error={errors.mac} />
            </Td>
            <Td>
              <InlineInput label={m("clientId")} value={row.client_id} onChange={(v) => setRow({ ...row, client_id: v })} error={errors.client_id} />
            </Td>
            <Td>
              <InlineInput label={m("hostname")} value={row.hostname} onChange={(v) => setRow({ ...row, hostname: v })} />
            </Td>
            <Td>
              {scopeId ? (
                scopeById.get(scopeId)?.name
              ) : (
                <select
                  aria-label={m("scope")}
                  value={row.scope_id}
                  onChange={(e) => setRow({ ...row, scope_id: Number(e.target.value) })}
                  className="rounded-md border border-slate-300 bg-white px-2 py-1 text-xs dark:border-slate-700 dark:bg-slate-900"
                >
                  <option value={0}>{m("scopeOption")}</option>
                  {scopes.data?.map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.name}
                    </option>
                  ))}
                </select>
              )}
              {errors.scope_id && <div className="text-xs text-red-600">{errors.scope_id}</div>}
            </Td>
            <Td>
              <InlineInput label={m("description")} value={row.description} onChange={(v) => setRow({ ...row, description: v })} />
            </Td>
            <Td className="text-right">
              <Button size="sm" variant="primary" onClick={submit} disabled={add.isPending}>
                {c("create")}
              </Button>
            </Td>
          </tr>
        )}
        {list.data?.map((r) => (
          <tr key={r.id}>
            <Td mono>{r.ip}</Td>
            <Td mono>{r.mac || "–"}</Td>
            <Td mono>{r.client_id || "–"}</Td>
            <Td>{r.hostname || "–"}</Td>
            <Td>{scopeById.get(r.scope_id)?.name}</Td>
            <Td className="max-w-xs truncate">{r.description}</Td>
            <Td className="text-right">
              <div className="flex justify-end gap-1">
                <Button size="sm" variant="ghost" onClick={() => setEdit(r)}>
                  {canWrite ? c("edit") : c("details")}
                </Button>
                {canWrite && (
                  <Button size="sm" variant="ghost" onClick={async () => (await confirm(m("confirmDelete", { ip: r.ip }), { danger: true })) && del.mutate(r.id)}>
                    {c("delete")}
                  </Button>
                )}
              </div>
            </Td>
          </tr>
        ))}
      </Table>
      {edit && <EditReservation res={edit} subnet={scopeById.get(edit.scope_id)?.subnet} onClose={() => setEdit(null)} />}
    </div>
  );
}

function InlineInput({ label, value, onChange, error }: { label: string; value: string; onChange: (v: string) => void; error?: string }) {
  return (
    <div>
      <input
        aria-label={label}
        placeholder={label}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        aria-invalid={!!error}
        className={`w-full min-w-24 rounded-md border bg-white px-2 py-1 font-mono text-xs dark:bg-slate-900 ${error ? "border-red-500" : "border-slate-300 dark:border-slate-700"}`}
      />
      {error && <div className="mt-0.5 text-xs whitespace-normal text-red-600">{error}</div>}
    </div>
  );
}

function EditReservation({ res, subnet, onClose }: { res: S["Reservation"]; subnet?: string; onClose: () => void }) {
  const { canWrite } = useAuth();
  const [f, setF] = useState(res);
  const [errors, setErrors] = useState<Errors>({});
  const qc = useQueryClient();
  const { toast } = useFeedback();
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT("/reservations/{id}", {
          params: { path: { id: res.id } },
          body: { scope_id: res.scope_id, ip: f.ip, mac: f.mac, client_id: f.client_id, hostname: f.hostname, description: f.description },
        }),
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["reservations"] });
      toast(m("saved"));
      onClose();
    },
    onError: (e) => {
      if (e instanceof ApiError) setErrors(e.fields);
      toast(errorMessage(e), "error");
    },
  });
  return (
    <Modal
      title={m("editTitle", { ip: res.ip })}
      onClose={onClose}
      wide
      footer={
        canWrite && (
          <Button
            variant="primary"
            onClick={() => {
              const e = validateReservation(f, subnet);
              setErrors(e);
              if (!hasErrors(e)) save.mutate();
            }}
          >
            {c("save")}
          </Button>
        )
      }
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Input label={m("ipAddress")} value={f.ip} onChange={(e) => setF({ ...f, ip: e.target.value })} error={errors.ip} disabled={!canWrite} />
        <Input label={m("hostname")} value={f.hostname} onChange={(e) => setF({ ...f, hostname: e.target.value })} disabled={!canWrite} />
        <Input label={m("macAddress")} value={f.mac} onChange={(e) => setF({ ...f, mac: e.target.value })} error={errors.mac} disabled={!canWrite} />
        <Input label={m("clientIdHex")} value={f.client_id} onChange={(e) => setF({ ...f, client_id: e.target.value })} error={errors.client_id} disabled={!canWrite} />
        <div className="sm:col-span-2">
          <Input label={m("description")} value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} disabled={!canWrite} />
        </div>
      </div>
      <Card title={m("options")}>
        <OptionsEditor owner="reservation" ownerId={res.id} />
      </Card>
    </Modal>
  );
}

function ImportDialog({ onClose }: { onClose: () => void }) {
  const [text, setText] = useState("scope,ip,mac,hostname,description\n");
  const [result, setResult] = useState<S["ImportResult"] | null>(null);
  const [error, setError] = useState("");
  const qc = useQueryClient();
  const run = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/reservations/import", {
          body: text,
          bodySerializer: (b: string) => b,
          headers: { "Content-Type": "text/csv" },
        }),
      ),
    onSuccess: (r) => {
      setResult(r);
      setError("");
      if (r.created > 0) qc.invalidateQueries({ queryKey: ["reservations"] });
    },
    onError: (e) => setError(errorMessage(e)),
  });
  return (
    <Modal
      title={m("importTitle")}
      onClose={onClose}
      wide
      footer={
        <>
          <Button onClick={onClose}>{c("close")}</Button>
          <Button variant="primary" onClick={() => run.mutate()} disabled={run.isPending}>
            {m("import")}
          </Button>
        </>
      }
    >
      <p className="text-sm text-slate-600 dark:text-slate-400">
        {m("columns")} <code>scope</code> {m("nameOrId")}, <code>ip</code>, <code>mac</code> {m("andOr")} <code>client_id</code>, {m("optional")} <code>hostname</code>,{" "}
        <code>description</code>. {m("csvRules")}
      </p>
      <input
        type="file"
        accept=".csv,text/csv"
        aria-label={m("csvFile")}
        className="text-sm"
        onChange={async (e) => {
          const file = e.target.files?.[0];
          if (file) setText(await file.text());
        }}
      />
      <TextArea label={m("csvContent")} rows={10} value={text} onChange={(e) => setText(e.target.value)} />
      {error && <Alert>{error}</Alert>}
      {result && result.errors.length === 0 && <Alert kind="success">{m("imported", { n: result.created })}</Alert>}
      {result && result.errors.length > 0 && (
        <Alert>
          {m("nothingImported")}
          <ul className="mt-1 list-disc pl-5">
            {result.errors.map((e, i) => (
              <li key={i}>{m("lineError", { line: e.line, message: e.message })}</li>
            ))}
          </ul>
        </Alert>
      )}
    </Modal>
  );
}

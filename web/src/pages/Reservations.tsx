import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { OptionsEditor } from "../components/OptionsEditor";
import { Alert, Button, Card, Input, Modal, PageHeader, Select, Spinner, Table, Td, TextArea, useFeedback } from "../components/ui";
import { hasErrors, validateReservation, type Errors } from "../lib/validate";
import { useScopes } from "./Scopes";

export function ReservationsPage() {
  const scopes = useScopes();
  const [scopeId, setScopeId] = useState(0);
  const [importOpen, setImportOpen] = useState(false);
  const { canWrite } = useAuth();
  return (
    <>
      <PageHeader
        title="Reservierungen"
        subtitle="Feste Adressen für bestimmte Clients (MAC oder Client-ID)"
        actions={canWrite && <Button onClick={() => setImportOpen(true)}>CSV-Import</Button>}
      />
      <div className="mb-4 max-w-xs">
        <Select label="Scope" value={String(scopeId)} onChange={(e) => setScopeId(Number(e.target.value))}>
          <option value="0">Alle Scopes</option>
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
      toast("Reservierung angelegt");
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
      toast("Reservierung gelöscht");
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });

  const submit = () => {
    const sid = row.scope_id || scopeId;
    const e = validateReservation(row, scopeById.get(sid)?.subnet);
    if (!sid) e.scope_id = "Scope wählen";
    setErrors(e);
    if (!hasErrors(e)) add.mutate();
  };

  if (list.isLoading) return <Spinner />;
  return (
    <div className="rounded-lg border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
      <Table head={["IP", "MAC", "Client-ID", "Hostname", "Scope", "Beschreibung", ""]} empty={list.data?.length === 0 && !canWrite}>
        {canWrite && (
          <tr className="bg-slate-50 dark:bg-slate-950/40">
            <Td>
              <InlineInput label="IP" value={row.ip} onChange={(v) => setRow({ ...row, ip: v })} error={errors.ip} />
            </Td>
            <Td>
              <InlineInput label="MAC" value={row.mac} onChange={(v) => setRow({ ...row, mac: v })} error={errors.mac} />
            </Td>
            <Td>
              <InlineInput label="Client-ID" value={row.client_id} onChange={(v) => setRow({ ...row, client_id: v })} error={errors.client_id} />
            </Td>
            <Td>
              <InlineInput label="Hostname" value={row.hostname} onChange={(v) => setRow({ ...row, hostname: v })} />
            </Td>
            <Td>
              {scopeId ? (
                scopeById.get(scopeId)?.name
              ) : (
                <select
                  aria-label="Scope"
                  value={row.scope_id}
                  onChange={(e) => setRow({ ...row, scope_id: Number(e.target.value) })}
                  className="rounded-md border border-slate-300 bg-white px-2 py-1 text-xs dark:border-slate-700 dark:bg-slate-900"
                >
                  <option value={0}>– Scope –</option>
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
              <InlineInput label="Beschreibung" value={row.description} onChange={(v) => setRow({ ...row, description: v })} />
            </Td>
            <Td className="text-right">
              <Button size="sm" variant="primary" onClick={submit} disabled={add.isPending}>
                Anlegen
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
                  {canWrite ? "Bearbeiten" : "Details"}
                </Button>
                {canWrite && (
                  <Button size="sm" variant="ghost" onClick={async () => (await confirm(`Reservierung ${r.ip} löschen?`, { danger: true })) && del.mutate(r.id)}>
                    Löschen
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
      toast("Reservierung gespeichert");
      onClose();
    },
    onError: (e) => {
      if (e instanceof ApiError) setErrors(e.fields);
      toast(errorMessage(e), "error");
    },
  });
  return (
    <Modal
      title={`Reservierung ${res.ip}`}
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
            Speichern
          </Button>
        )
      }
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Input label="IP-Adresse" value={f.ip} onChange={(e) => setF({ ...f, ip: e.target.value })} error={errors.ip} disabled={!canWrite} />
        <Input label="Hostname" value={f.hostname} onChange={(e) => setF({ ...f, hostname: e.target.value })} disabled={!canWrite} />
        <Input label="MAC-Adresse" value={f.mac} onChange={(e) => setF({ ...f, mac: e.target.value })} error={errors.mac} disabled={!canWrite} />
        <Input label="Client-ID (hex)" value={f.client_id} onChange={(e) => setF({ ...f, client_id: e.target.value })} error={errors.client_id} disabled={!canWrite} />
        <div className="sm:col-span-2">
          <Input label="Beschreibung" value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} disabled={!canWrite} />
        </div>
      </div>
      <Card title="Reservierungs-Optionen (höchste Priorität)">
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
      title="Reservierungen importieren (CSV)"
      onClose={onClose}
      wide
      footer={
        <>
          <Button onClick={onClose}>Schließen</Button>
          <Button variant="primary" onClick={() => run.mutate()} disabled={run.isPending}>
            Importieren
          </Button>
        </>
      }
    >
      <p className="text-sm text-slate-600 dark:text-slate-400">
        Spalten: <code>scope</code> (Name oder ID), <code>ip</code>, <code>mac</code> und/oder <code>client_id</code>, optional <code>hostname</code>, <code>description</code>. Trennzeichen
        Komma oder Semikolon. Es wird alles oder nichts importiert.
      </p>
      <input
        type="file"
        accept=".csv,text/csv"
        aria-label="CSV-Datei"
        className="text-sm"
        onChange={async (e) => {
          const file = e.target.files?.[0];
          if (file) setText(await file.text());
        }}
      />
      <TextArea label="CSV-Inhalt" rows={10} value={text} onChange={(e) => setText(e.target.value)} />
      {error && <Alert>{error}</Alert>}
      {result && result.errors.length === 0 && <Alert kind="success">{result.created} Reservierungen importiert.</Alert>}
      {result && result.errors.length > 0 && (
        <Alert>
          Nichts importiert – bitte korrigieren:
          <ul className="mt-1 list-disc pl-5">
            {result.errors.map((e, i) => (
              <li key={i}>
                Zeile {e.line}: {e.message}
              </li>
            ))}
          </ul>
        </Alert>
      )}
    </Modal>
  );
}

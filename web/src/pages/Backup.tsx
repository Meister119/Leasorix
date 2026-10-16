import { useState, type ReactNode } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { Alert, Button, Card, Select, Toggle, useFeedback } from "../components/ui";
import { downloadFile, fileStamp } from "../lib/download";

/** File picker styled as a button. */
function FileButton({ label, accept, onFile, testId }: { label: string; accept: string; onFile: (f: File) => void; testId?: string }) {
  return (
    <label className="inline-flex cursor-pointer items-center rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm font-medium hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:hover:bg-slate-800">
      {label}
      <input
        type="file"
        accept={accept}
        className="sr-only"
        data-testid={testId}
        onChange={(e) => {
          const f = e.target.files?.[0];
          if (f) onFile(f);
          e.target.value = "";
        }}
      />
    </label>
  );
}

function ImportSummary({ r }: { r: S["ConfigImportResult"] }) {
  const parts: [number, string][] = [
    [r.scopes, "Scopes"],
    [r.reservations, "Reservierungen"],
    [r.policies, "Policies"],
    [r.classes, "Klassen"],
    [r.option_definitions, "Optionsdefinitionen"],
    [r.options, "Optionswerte"],
    [r.mac_filters, "MAC-Filter"],
    [r.superscopes, "Superscopes"],
    [r.dns_zones, "DNS-Zonen"],
  ];
  return (
    <Alert kind="success">
      {r.dry_run ? "Prüfung erfolgreich – es wurde nichts geändert. " : "Importiert: "}
      {parts
        .filter(([n]) => n > 0)
        .map(([n, l]) => `${n} ${l}`)
        .join(", ") || "keine Objekte"}
      .
    </Alert>
  );
}

function ImportOptions({ mode, setMode, dry, setDry }: { mode: "merge" | "replace"; setMode: (m: "merge" | "replace") => void; dry: boolean; setDry: (v: boolean) => void }) {
  return (
    <div className="grid gap-3 sm:grid-cols-2 sm:items-end">
      <Select
        label="Modus"
        value={mode}
        onChange={(e) => setMode(e.target.value as "merge" | "replace")}
        hint={mode === "merge" ? "vorhandene Objekte führen zu einem Konflikt" : "ersetzt die gesamte DHCP-Konfiguration"}
      >
        <option value="merge">Ergänzen</option>
        <option value="replace">Ersetzen</option>
      </Select>
      <div className="pb-2">
        <Toggle label="Nur prüfen (Dry-Run)" checked={dry} onChange={setDry} />
      </div>
    </div>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return <Card title={title}>{children}</Card>;
}

export function BackupImport() {
  const { isAdmin, configReadOnly } = useAuth();
  const canImport = isAdmin && !configReadOnly;
  return (
    <div className="grid gap-6 lg:grid-cols-2">
      <ConfigExport />
      {canImport && <ConfigImport />}
      {canImport && <NetshImport />}
      {isAdmin && <BackupRestore readOnly={configReadOnly} />}
      {!isAdmin && (
        <Alert kind="info">Import, Backup und Wiederherstellung sind Administratoren vorbehalten.</Alert>
      )}
    </div>
  );
}

function ConfigExport() {
  const { toast } = useFeedback();
  const run = async (format: "yaml" | "json") => {
    try {
      const text = await unwrap(api.GET("/config/export", { params: { query: { format } }, parseAs: "text" }));
      downloadFile(`leasorix-config-${fileStamp()}.${format}`, text as unknown as string, format === "yaml" ? "application/yaml" : "application/json");
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };
  return (
    <Section title="Konfiguration exportieren">
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        Die komplette DHCP-Konfiguration (Scopes, Superscopes, Ausschlüsse, Reservierungen, Optionen, Klassen, Policies, Filter) als Datei.
        Benutzer und API-Tokens sind nicht enthalten.
      </p>
      <div className="flex gap-2">
        <Button onClick={() => run("yaml")}>YAML</Button>
        <Button onClick={() => run("json")}>JSON</Button>
      </div>
    </Section>
  );
}

function useImportMutation<T>(fn: (f: File, mode: "merge" | "replace", dry: boolean) => Promise<T>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { f: File; mode: "merge" | "replace"; dry: boolean }) => fn(v.f, v.mode, v.dry),
    onSuccess: () => qc.invalidateQueries(),
  });
}

function ConfigImport() {
  const { confirm } = useFeedback();
  const [mode, setMode] = useState<"merge" | "replace">("merge");
  const [dry, setDry] = useState(true);
  const [result, setResult] = useState<S["ConfigImportResult"] | null>(null);
  const [err, setErr] = useState("");
  const imp = useImportMutation(async (f, mode, dry) => {
    const text = await f.text();
    const json = text.trimStart().startsWith("{");
    return unwrap(
      api.POST("/config/import", {
        params: { query: { mode, dry_run: dry } },
        body: text as never,
        bodySerializer: (b: unknown) => b as string,
        headers: { "Content-Type": json ? "application/json" : "application/yaml" },
      }),
    );
  });
  const onFile = async (f: File) => {
    if (mode === "replace" && !dry && !(await confirm("Die gesamte DHCP-Konfiguration wird ersetzt. Fortfahren?", { danger: true, confirmLabel: "Ersetzen" }))) return;
    setErr("");
    setResult(null);
    imp.mutate({ f, mode, dry }, { onSuccess: setResult, onError: (e) => setErr(errorMessage(e)) });
  };
  return (
    <Section title="Konfiguration importieren">
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        YAML- oder JSON-Export einer Leasorix-Instanz. Alles wird vorab vollständig geprüft; bei einem Fehler wird nichts übernommen.
      </p>
      <ImportOptions mode={mode} setMode={setMode} dry={dry} setDry={setDry} />
      <div className="mt-3">
        <FileButton label="Datei wählen…" accept=".yaml,.yml,.json,application/json,application/yaml" onFile={onFile} testId="config-import" />
      </div>
      <div className="mt-3 space-y-2">
        {err && <Alert>{err}</Alert>}
        {result && <ImportSummary r={result} />}
      </div>
    </Section>
  );
}

function NetshImport() {
  const [mode, setMode] = useState<"merge" | "replace">("merge");
  const [dry, setDry] = useState(true);
  const [result, setResult] = useState<S["NetshImportResult"] | null>(null);
  const [err, setErr] = useState("");
  const imp = useImportMutation(async (f, mode, dry) => {
    // Dumps from PowerShell are UTF-16; the server converts them.
    const buf = await f.arrayBuffer();
    return unwrap(
      api.POST("/config/import/netsh", {
        params: { query: { mode, dry_run: dry } },
        body: buf as never,
        bodySerializer: (b: unknown) => b as ArrayBuffer,
        headers: { "Content-Type": "text/plain" },
      }),
    );
  });
  return (
    <Section title="Windows-DHCP übernehmen (netsh)">
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        Ausgabe von <code>netsh dhcp server dump &gt; dhcp.txt</code> eines Windows-Servers. Übernommen werden Scopes, Bereiche, Ausschlüsse,
        Reservierungen, Optionswerte, eigene Optionsdefinitionen, Klassen, Superscopes und MAC-Filter.
      </p>
      <ImportOptions mode={mode} setMode={setMode} dry={dry} setDry={setDry} />
      <div className="mt-3">
        <FileButton
          label="Dump wählen…"
          accept=".txt,text/plain"
          testId="netsh-import"
          onFile={(f) => {
            setErr("");
            setResult(null);
            imp.mutate({ f, mode, dry }, { onSuccess: setResult, onError: (e) => setErr(errorMessage(e)) });
          }}
        />
      </div>
      <div className="mt-3 space-y-2">
        {err && <Alert>{err}</Alert>}
        {result && <ImportSummary r={result.result} />}
        {result && result.warnings.length > 0 && (
          <details className="text-sm">
            <summary className="cursor-pointer text-slate-600 dark:text-slate-400">{result.warnings.length} Zeilen nicht übernommen</summary>
            <ul className="mt-2 max-h-64 space-y-1 overflow-y-auto text-xs">
              {result.warnings.map((w, i) => (
                <li key={i}>
                  {w.line > 0 && <span className="font-mono text-slate-500">Zeile {w.line}: </span>}
                  {w.reason}
                  {w.line === 0 && <span className="text-slate-500"> ({w.text})</span>}
                </li>
              ))}
            </ul>
          </details>
        )}
      </div>
    </Section>
  );
}

function BackupRestore({ readOnly }: { readOnly: boolean }) {
  const { toast, confirm } = useFeedback();
  const [busy, setBusy] = useState(false);
  const [restored, setRestored] = useState<S["RestoreResult"] | null>(null);
  const [err, setErr] = useState("");
  const download = async () => {
    setBusy(true);
    try {
      const blob = (await unwrap(api.GET("/system/backup", { parseAs: "blob" }))) as unknown as Blob;
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `leasorix-backup-${fileStamp()}.db`;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setBusy(false);
    }
  };
  const restore = async (f: File) => {
    if (
      !(await confirm(
        `Konfiguration, Benutzer, API-Tokens und Leases durch den Stand aus „${f.name}“ ersetzen? Alle Sitzungen werden beendet.`,
        { danger: true, confirmLabel: "Wiederherstellen" },
      ))
    )
      return;
    setErr("");
    try {
      const r = await unwrap(
        api.POST("/system/restore", {
          body: f as never,
          bodySerializer: (b: unknown) => b as Blob,
          headers: { "Content-Type": "application/octet-stream" },
        }),
      );
      setRestored(r);
    } catch (e) {
      setErr(errorMessage(e));
    }
  };
  return (
    <Section title="Backup & Wiederherstellung">
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        Das Backup ist eine vollständige Kopie der Datenbank (inkl. Passwort-Hashes – sicher aufbewahren). Wiederherstellen übernimmt Konfiguration,
        Benutzer, API-Tokens und Leases; auch Backups älterer Versionen. CLI: <code>leasorixd backup|restore</code>, <code>leasorixctl backup</code>.
      </p>
      <div className="flex flex-wrap gap-2">
        <Button onClick={download} disabled={busy}>
          Backup herunterladen
        </Button>
        {!readOnly && <FileButton label="Wiederherstellen…" accept=".db,application/octet-stream" onFile={restore} testId="restore" />}
      </div>
      <div className="mt-3 space-y-2">
        {err && <Alert>{err}</Alert>}
        {restored && (
          <Alert kind="success">
            Wiederhergestellt: {restored.scopes} Scopes, {restored.reservations} Reservierungen, {restored.users} Benutzer, {restored.leases} Leases.{" "}
            <button type="button" className="font-medium underline" onClick={() => window.location.reload()}>
              Neu anmelden
            </button>
          </Alert>
        )}
      </div>
    </Section>
  );
}


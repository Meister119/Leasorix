import { useState, type ReactNode } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { Alert, Button, Card, Select, Toggle, useFeedback } from "../components/ui";
import { messages } from "../i18n";
import { downloadFile, fileStamp } from "../lib/download";

const m = messages({
  en: {
    scopes: "scopes",
    reservations: "reservations",
    policies: "policies",
    classes: "classes",
    optionDefinitions: "option definitions",
    optionValues: "option values",
    macFilters: "MAC filters",
    superscopes: "superscopes",
    dnsZones: "DNS zones",
    ipv6Scopes: "IPv6 scopes",
    dryRunOk: "Dry run successful – nothing was changed. ",
    imported: "Imported: ",
    noObjects: "no objects",
    mode: "Mode",
    mergeHint: "existing objects cause a conflict",
    replaceHint: "replaces the entire DHCP configuration",
    merge: "Merge",
    replace: "Replace",
    dryRun: "Dry run",
    adminOnly: "Only administrators can import, back up and restore.",
    exportTitle: "Export configuration",
    exportInfo:
      "The complete DHCP configuration (scopes, superscopes, exclusions, reservations, options, classes, policies, filters, DNS zones, IPv6 scopes) as a file. Users and API tokens are not included.",
    replaceConfirm: "The entire DHCP configuration will be replaced. Continue?",
    importTitle: "Import configuration",
    importInfo: "YAML or JSON export of a Leasorix instance. Everything is fully validated first; if there is an error, nothing is applied.",
    chooseFile: "Choose file…",
    netshTitle: "Import from Windows DHCP (netsh)",
    netshInfo:
      "Output of {cmd} on a Windows server. Scopes, ranges, exclusions, reservations, option values, custom option definitions, classes, superscopes and MAC filters are imported.",
    chooseDump: "Choose dump…",
    linesSkipped: "{n} lines not imported",
    line: "Line {n}: ",
    restoreConfirm: "Replace configuration, users, API tokens and leases with the state from “{name}”? All sessions will be signed out.",
    restore: "Restore",
    backupTitle: "Backup & restore",
    backupInfo:
      "The backup is a complete copy of the database (including password hashes – keep it safe). Restoring applies configuration, users, API tokens and leases; backups from older versions work too.",
    downloadBackup: "Download backup",
    restoreFile: "Restore…",
    restored: "Restored: {scopes} scopes, {reservations} reservations, {users} users, {leases} leases.",
    signInAgain: "Sign in again",
  },
  de: {
    scopes: "Scopes",
    reservations: "Reservierungen",
    policies: "Policies",
    classes: "Klassen",
    optionDefinitions: "Optionsdefinitionen",
    optionValues: "Optionswerte",
    macFilters: "MAC-Filter",
    superscopes: "Superscopes",
    dnsZones: "DNS-Zonen",
    ipv6Scopes: "IPv6-Scopes",
    dryRunOk: "Prüfung erfolgreich – es wurde nichts geändert. ",
    imported: "Importiert: ",
    noObjects: "keine Objekte",
    mode: "Modus",
    mergeHint: "vorhandene Objekte führen zu einem Konflikt",
    replaceHint: "ersetzt die gesamte DHCP-Konfiguration",
    merge: "Ergänzen",
    replace: "Ersetzen",
    dryRun: "Nur prüfen (Dry-Run)",
    adminOnly: "Import, Backup und Wiederherstellung sind Administratoren vorbehalten.",
    exportTitle: "Konfiguration exportieren",
    exportInfo:
      "Die komplette DHCP-Konfiguration (Scopes, Superscopes, Ausschlüsse, Reservierungen, Optionen, Klassen, Policies, Filter, DNS-Zonen, IPv6-Scopes) als Datei. Benutzer und API-Tokens sind nicht enthalten.",
    replaceConfirm: "Die gesamte DHCP-Konfiguration wird ersetzt. Fortfahren?",
    importTitle: "Konfiguration importieren",
    importInfo: "YAML- oder JSON-Export einer Leasorix-Instanz. Alles wird vorab vollständig geprüft; bei einem Fehler wird nichts übernommen.",
    chooseFile: "Datei wählen…",
    netshTitle: "Windows-DHCP übernehmen (netsh)",
    netshInfo:
      "Ausgabe von {cmd} eines Windows-Servers. Übernommen werden Scopes, Bereiche, Ausschlüsse, Reservierungen, Optionswerte, eigene Optionsdefinitionen, Klassen, Superscopes und MAC-Filter.",
    chooseDump: "Dump wählen…",
    linesSkipped: "{n} Zeilen nicht übernommen",
    line: "Zeile {n}: ",
    restoreConfirm: "Konfiguration, Benutzer, API-Tokens und Leases durch den Stand aus „{name}“ ersetzen? Alle Sitzungen werden beendet.",
    restore: "Wiederherstellen",
    backupTitle: "Backup & Wiederherstellung",
    backupInfo:
      "Das Backup ist eine vollständige Kopie der Datenbank (inkl. Passwort-Hashes – sicher aufbewahren). Wiederherstellen übernimmt Konfiguration, Benutzer, API-Tokens und Leases; auch Backups älterer Versionen.",
    downloadBackup: "Backup herunterladen",
    restoreFile: "Wiederherstellen…",
    restored: "Wiederhergestellt: {scopes} Scopes, {reservations} Reservierungen, {users} Benutzer, {leases} Leases.",
    signInAgain: "Neu anmelden",
  },
});

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
    [r.scopes, m("scopes")],
    [r.reservations, m("reservations")],
    [r.policies, m("policies")],
    [r.classes, m("classes")],
    [r.option_definitions, m("optionDefinitions")],
    [r.options, m("optionValues")],
    [r.mac_filters, m("macFilters")],
    [r.superscopes, m("superscopes")],
    [r.dns_zones, m("dnsZones")],
    [r.scopes6, m("ipv6Scopes")],
  ];
  return (
    <Alert kind="success">
      {r.dry_run ? m("dryRunOk") : m("imported")}
      {parts
        .filter(([n]) => n > 0)
        .map(([n, l]) => `${n} ${l}`)
        .join(", ") || m("noObjects")}
      .
    </Alert>
  );
}

function ImportOptions({ mode, setMode, dry, setDry }: { mode: "merge" | "replace"; setMode: (m: "merge" | "replace") => void; dry: boolean; setDry: (v: boolean) => void }) {
  return (
    <div className="grid gap-3 sm:grid-cols-2 sm:items-end">
      <Select
        label={m("mode")}
        value={mode}
        onChange={(e) => setMode(e.target.value as "merge" | "replace")}
        hint={mode === "merge" ? m("mergeHint") : m("replaceHint")}
      >
        <option value="merge">{m("merge")}</option>
        <option value="replace">{m("replace")}</option>
      </Select>
      <div className="pb-2">
        <Toggle label={m("dryRun")} checked={dry} onChange={setDry} />
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
        <Alert kind="info">{m("adminOnly")}</Alert>
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
    <Section title={m("exportTitle")}>
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">{m("exportInfo")}</p>
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
    if (mode === "replace" && !dry && !(await confirm(m("replaceConfirm"), { danger: true, confirmLabel: m("replace") }))) return;
    setErr("");
    setResult(null);
    imp.mutate({ f, mode, dry }, { onSuccess: setResult, onError: (e) => setErr(errorMessage(e)) });
  };
  return (
    <Section title={m("importTitle")}>
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">{m("importInfo")}</p>
      <ImportOptions mode={mode} setMode={setMode} dry={dry} setDry={setDry} />
      <div className="mt-3">
        <FileButton label={m("chooseFile")} accept=".yaml,.yml,.json,application/json,application/yaml" onFile={onFile} testId="config-import" />
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
  const [infoBefore, infoAfter] = m("netshInfo").split("{cmd}");
  return (
    <Section title={m("netshTitle")}>
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        {infoBefore}
        <code>netsh dhcp server dump &gt; dhcp.txt</code>
        {infoAfter}
      </p>
      <ImportOptions mode={mode} setMode={setMode} dry={dry} setDry={setDry} />
      <div className="mt-3">
        <FileButton
          label={m("chooseDump")}
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
            <summary className="cursor-pointer text-slate-600 dark:text-slate-400">{m("linesSkipped", { n: result.warnings.length })}</summary>
            <ul className="mt-2 max-h-64 space-y-1 overflow-y-auto text-xs">
              {result.warnings.map((w, i) => (
                <li key={i}>
                  {w.line > 0 && <span className="font-mono text-slate-500">{m("line", { n: w.line })}</span>}
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
        m("restoreConfirm", { name: f.name }),
        { danger: true, confirmLabel: m("restore") },
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
    <Section title={m("backupTitle")}>
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        {m("backupInfo")} CLI: <code>leasorixd backup|restore</code>, <code>leasorixctl backup</code>.
      </p>
      <div className="flex flex-wrap gap-2">
        <Button onClick={download} disabled={busy}>
          {m("downloadBackup")}
        </Button>
        {!readOnly && <FileButton label={m("restoreFile")} accept=".db,application/octet-stream" onFile={restore} testId="restore" />}
      </div>
      <div className="mt-3 space-y-2">
        {err && <Alert>{err}</Alert>}
        {restored && (
          <Alert kind="success">
            {m("restored", { scopes: restored.scopes, reservations: restored.reservations, users: restored.users, leases: restored.leases })}{" "}
            <button type="button" className="font-medium underline" onClick={() => window.location.reload()}>
              {m("signInAgain")}
            </button>
          </Alert>
        )}
      </div>
    </Section>
  );
}


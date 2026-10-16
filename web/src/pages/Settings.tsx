import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { BackupImport } from "./Backup";
import { OptionsEditor, useOptionDefinitions } from "../components/OptionsEditor";
import { Alert, Badge, Button, Card, Input, Modal, PageHeader, Select, Table, Tabs, Td, Toggle, useFeedback } from "../components/ui";
import { formatDateTime, roleLabel } from "../lib/format";

type Tab = "account" | "users" | "options" | "backup" | "system";

export function SettingsPage() {
  const { isAdmin } = useAuth();
  const [tab, setTab] = useState<Tab>("account");
  const tabs: { id: Tab; label: string }[] = [
    { id: "account", label: "Mein Konto & API-Tokens" },
    ...(isAdmin ? [{ id: "users" as Tab, label: "Benutzer" }] : []),
    { id: "options", label: "Server-Optionen" },
    { id: "backup", label: "Sicherung & Import" },
    { id: "system", label: "System" },
  ];
  return (
    <>
      <PageHeader title="Benutzer & Einstellungen" />
      <Tabs<Tab> tabs={tabs} value={tab} onChange={setTab} />
      {tab === "account" && <Account />}
      {tab === "users" && isAdmin && <Users />}
      {tab === "options" && <ServerOptions />}
      {tab === "backup" && <BackupImport />}
      {tab === "system" && <SystemInfo />}
    </>
  );
}

function Account() {
  const { toast } = useFeedback();
  const [cur, setCur] = useState("");
  const [next, setNext] = useState("");
  const [err, setErr] = useState("");
  const pw = useMutation({
    mutationFn: () => unwrap(api.POST("/auth/password", { body: { current_password: cur, new_password: next } })),
    onSuccess: () => {
      setCur("");
      setNext("");
      setErr("");
      toast("Passwort geändert; andere Sitzungen wurden beendet");
    },
    onError: (e) => setErr(errorMessage(e)),
  });
  return (
    <div className="grid gap-6 lg:grid-cols-2">
      <Card title="Passwort ändern">
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (next.length < 10) return setErr("Neues Passwort: mindestens 10 Zeichen");
            pw.mutate();
          }}
        >
          {err && <Alert>{err}</Alert>}
          <Input label="Aktuelles Passwort" type="password" autoComplete="current-password" value={cur} onChange={(e) => setCur(e.target.value)} />
          <Input label="Neues Passwort" type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} />
          <Button type="submit" variant="primary">
            Ändern
          </Button>
        </form>
      </Card>
      <Tokens />
    </div>
  );
}

function Tokens() {
  const { user } = useAuth();
  const qc = useQueryClient();
  const { toast, confirm } = useFeedback();
  const list = useQuery({ queryKey: ["tokens"], queryFn: () => unwrap(api.GET("/tokens")) });
  const [name, setName] = useState("");
  const [role, setRole] = useState<S["Role"] | "">("");
  const [days, setDays] = useState("");
  const [created, setCreated] = useState<string | null>(null);
  const add = useMutation({
    mutationFn: () =>
      unwrap(api.POST("/tokens", { body: { name, ...(role ? { role } : {}), ...(days ? { expires_in_days: Number(days) } : {}) } })),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ["tokens"] });
      setCreated(r.token);
      setName("");
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const del = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/tokens/{id}", { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["tokens"] }),
  });
  const roles: S["Role"][] = user?.role === "admin" ? ["admin", "operator", "readonly"] : user?.role === "operator" ? ["operator", "readonly"] : ["readonly"];
  return (
    <Card title="API-Tokens">
      {created && (
        <Alert kind="success">
          Token erstellt – er wird nur jetzt angezeigt:
          <code className="mt-1 block break-all">{created}</code>
        </Alert>
      )}
      <Table head={["Name", "Präfix", "Rolle", "Läuft ab", "Zuletzt benutzt", ""]} empty={list.data?.length === 0}>
        {list.data?.map((t) => (
          <tr key={t.id}>
            <Td>{t.name}</Td>
            <Td mono>{t.prefix}…</Td>
            <Td>{roleLabel[t.role]}</Td>
            <Td>{t.expires_at ? formatDateTime(t.expires_at) : "nie"}</Td>
            <Td>{formatDateTime(t.last_used_at)}</Td>
            <Td className="text-right">
              <Button size="sm" variant="ghost" onClick={async () => (await confirm(`Token „${t.name}“ widerrufen?`, { danger: true })) && del.mutate(t.id)}>
                Widerrufen
              </Button>
            </Td>
          </tr>
        ))}
      </Table>
      <form
        className="mt-4 grid gap-3 sm:grid-cols-[2fr_1fr_1fr_auto] sm:items-end"
        onSubmit={(e) => {
          e.preventDefault();
          add.mutate();
        }}
      >
        <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} required />
        <Select label="Rolle" value={role} onChange={(e) => setRole(e.target.value as S["Role"])}>
          <option value="">wie Benutzer</option>
          {roles.map((r) => (
            <option key={r} value={r}>
              {roleLabel[r]}
            </option>
          ))}
        </Select>
        <Input label="Gültig (Tage)" type="number" min={1} value={days} onChange={(e) => setDays(e.target.value)} placeholder="unbegrenzt" />
        <Button type="submit" variant="primary">
          Erstellen
        </Button>
      </form>
    </Card>
  );
}

function Users() {
  const { user: me } = useAuth();
  const qc = useQueryClient();
  const { toast, confirm } = useFeedback();
  const list = useQuery({ queryKey: ["users"], queryFn: () => unwrap(api.GET("/users")) });
  const [f, setF] = useState({ username: "", password: "", role: "readonly" as S["Role"] });
  const [reset, setReset] = useState<S["User"] | null>(null);
  const [newPw, setNewPw] = useState("");
  const refresh = () => qc.invalidateQueries({ queryKey: ["users"] });
  const add = useMutation({
    mutationFn: () => unwrap(api.POST("/users", { body: f })),
    onSuccess: () => {
      refresh();
      setF({ username: "", password: "", role: "readonly" });
      toast("Benutzer angelegt");
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const upd = useMutation({
    mutationFn: (v: { id: number; body: S["UserUpdate"] }) => unwrap(api.PATCH("/users/{id}", { params: { path: { id: v.id } }, body: v.body })),
    onSuccess: () => {
      refresh();
      toast("Benutzer aktualisiert");
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const del = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/users/{id}", { params: { path: { id } } })),
    onSuccess: refresh,
    onError: (e) => toast(errorMessage(e), "error"),
  });
  return (
    <Card title="Benutzer">
      <Table head={["Benutzername", "Rolle", "Status", "Angelegt", ""]}>
        {list.data?.map((u) => (
          <tr key={u.id}>
            <Td className="font-medium">
              {u.username} {u.id === me?.id && <Badge color="teal">Sie</Badge>}
            </Td>
            <Td>
              <select
                aria-label={`Rolle von ${u.username}`}
                value={u.role}
                onChange={(e) => upd.mutate({ id: u.id, body: { role: e.target.value as S["Role"] } })}
                className="rounded-md border border-slate-300 bg-white px-2 py-1 text-xs dark:border-slate-700 dark:bg-slate-900"
              >
                {(["admin", "operator", "readonly"] as const).map((r) => (
                  <option key={r} value={r}>
                    {roleLabel[r]}
                  </option>
                ))}
              </select>
            </Td>
            <Td>
              <Toggle label={u.disabled ? "deaktiviert" : "aktiv"} checked={!u.disabled} onChange={(v) => upd.mutate({ id: u.id, body: { disabled: !v } })} />
            </Td>
            <Td>{formatDateTime(u.created_at)}</Td>
            <Td className="text-right">
              <div className="flex justify-end gap-1">
                <Button size="sm" variant="ghost" onClick={() => setReset(u)}>
                  Passwort setzen
                </Button>
                {u.id !== me?.id && (
                  <Button size="sm" variant="ghost" onClick={async () => (await confirm(`Benutzer „${u.username}“ löschen?`, { danger: true })) && del.mutate(u.id)}>
                    Löschen
                  </Button>
                )}
              </div>
            </Td>
          </tr>
        ))}
      </Table>
      <form
        className="mt-4 grid gap-3 sm:grid-cols-[1fr_1fr_1fr_auto] sm:items-end"
        onSubmit={(e) => {
          e.preventDefault();
          add.mutate();
        }}
      >
        <Input label="Benutzername" value={f.username} onChange={(e) => setF({ ...f, username: e.target.value })} required />
        <Input label="Passwort" type="password" autoComplete="new-password" value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} hint="mind. 10 Zeichen" required />
        <Select label="Rolle" value={f.role} onChange={(e) => setF({ ...f, role: e.target.value as S["Role"] })}>
          {(["readonly", "operator", "admin"] as const).map((r) => (
            <option key={r} value={r}>
              {roleLabel[r]}
            </option>
          ))}
        </Select>
        <Button type="submit" variant="primary">
          Anlegen
        </Button>
      </form>
      {reset && (
        <Modal
          title={`Passwort für ${reset.username}`}
          onClose={() => setReset(null)}
          footer={
            <Button
              variant="primary"
              onClick={() => {
                upd.mutate({ id: reset.id, body: { password: newPw } });
                setReset(null);
                setNewPw("");
              }}
            >
              Setzen
            </Button>
          }
        >
          <Input label="Neues Passwort" type="password" autoComplete="new-password" value={newPw} onChange={(e) => setNewPw(e.target.value)} hint="Bestehende Sitzungen des Benutzers werden beendet" />
        </Modal>
      )}
    </Card>
  );
}

function ServerOptions() {
  const { canWrite } = useAuth();
  const defs = useOptionDefinitions();
  const qc = useQueryClient();
  const { toast } = useFeedback();
  const [f, setF] = useState({ code: "", name: "", type: "string" as S["OptionType"] });
  const add = useMutation({
    mutationFn: () => unwrap(api.POST("/option-definitions", { body: { code: Number(f.code), name: f.name, type: f.type } })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["option-definitions"] });
      setF({ code: "", name: "", type: "string" });
      toast("Optionsdefinition angelegt");
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const del = useMutation({
    mutationFn: (code: number) => unwrap(api.DELETE("/option-definitions/{code}", { params: { path: { code } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["option-definitions"] }),
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const custom = defs.data?.filter((d) => !d.builtin) ?? [];
  return (
    <div className="space-y-6">
      <Card title="Server-Optionen" actions={<span className="text-xs text-slate-500">gelten für alle Scopes, sofern nicht überschrieben</span>}>
        <OptionsEditor owner="server" ownerId={0} />
      </Card>
      <Card title="Eigene Optionsdefinitionen">
        <Table head={["Code", "Name", "Typ", ""]} empty={custom.length === 0}>
          {custom.map((d) => (
            <tr key={d.code}>
              <Td mono>{d.code}</Td>
              <Td>{d.name}</Td>
              <Td>
                <Badge>{d.type}</Badge>
              </Td>
              <Td className="text-right">
                {canWrite && (
                  <Button size="sm" variant="ghost" onClick={() => del.mutate(d.code)}>
                    Löschen
                  </Button>
                )}
              </Td>
            </tr>
          ))}
        </Table>
        {canWrite && (
          <form
            className="mt-4 grid gap-3 sm:grid-cols-[1fr_2fr_1fr_auto] sm:items-end"
            onSubmit={(e) => {
              e.preventDefault();
              add.mutate();
            }}
          >
            <Input label="Code" type="number" min={1} max={254} value={f.code} onChange={(e) => setF({ ...f, code: e.target.value })} required />
            <Input label="Name" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} required />
            <Select label="Typ" value={f.type} onChange={(e) => setF({ ...f, type: e.target.value as S["OptionType"] })}>
              {(["ip", "ip-list", "string", "uint8", "uint16", "uint32", "int32", "bool", "hex", "domain-list", "routes"] as const).map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
            </Select>
            <Button type="submit" variant="primary">
              Definieren
            </Button>
          </form>
        )}
      </Card>
    </div>
  );
}

function SystemInfo() {
  const info = useQuery({ queryKey: ["system"], queryFn: () => unwrap(api.GET("/system")) });
  const i = info.data;
  return (
    <Card title="System">
      {i && (
        <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
          <dt className="text-slate-500">Version</dt>
          <dd>
            {i.version} ({i.commit})
          </dd>
          <dt className="text-slate-500">Go</dt>
          <dd>{i.go_version}</dd>
          <dt className="text-slate-500">Gestartet</dt>
          <dd>{formatDateTime(i.started_at)}</dd>
          <dt className="text-slate-500">DHCPv4-Interfaces</dt>
          <dd className="font-mono">{i.dhcp4_interfaces.join(", ") || "keine (DHCP deaktiviert)"}</dd>
          <dt className="text-slate-500">HTTPS</dt>
          <dd>{i.tls ? "ja" : "nein"}</dd>
          <dt className="text-slate-500">Konflikterkennung</dt>
          <dd>{i.conflict_detection ? "aktiv (ICMP)" : "aus"}</dd>
        </dl>
      )}
    </Card>
  );
}

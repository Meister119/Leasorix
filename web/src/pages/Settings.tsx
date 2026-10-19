import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { BackupImport } from "./Backup";
import { OptionsEditor, useOptionDefinitions } from "../components/OptionsEditor";
import { Alert, Badge, Button, Card, Input, Modal, PageHeader, Select, Table, Tabs, Td, Toggle, useFeedback } from "../components/ui";
import { messages } from "../i18n";
import { c } from "../i18n/common";
import { formatDateTime, roleLabel } from "../lib/format";

const m = messages({
  en: {
    title: "Users & settings",
    tabAccount: "My account & API tokens",
    tabUsers: "Users",
    tabOptions: "Server options",
    tabBackup: "Backup & import",
    tabSystem: "System",
    passwordChanged: "Password changed; other sessions were signed out",
    changePassword: "Change password",
    newPasswordTooShort: "New password: at least 10 characters",
    currentPassword: "Current password",
    newPassword: "New password",
    change: "Change",
    apiTokens: "API tokens",
    tokenCreated: "Token created – it is shown only this once:",
    name: "Name",
    prefix: "Prefix",
    role: "Role",
    expires: "Expires",
    lastUsed: "Last used",
    never: "never",
    revokeToken: "Revoke token “{name}”?",
    revoke: "Revoke",
    sameAsUser: "same as user",
    validDays: "Valid for (days)",
    createToken: "Create",
    userCreated: "User created",
    userUpdated: "User updated",
    users: "Users",
    username: "Username",
    status: "Status",
    created: "Created",
    you: "You",
    roleOf: "Role of {name}",
    setPassword: "Set password",
    deleteUser: "Delete user “{name}”?",
    password: "Password",
    minLength: "at least 10 characters",
    passwordFor: "Password for {name}",
    sessionsEnded: "The user’s existing sessions will be signed out",
    definitionCreated: "Option definition created",
    serverOptions: "Server options",
    serverOptionsHint: "apply to all scopes unless overridden",
    customDefinitions: "Custom option definitions",
    code: "Code",
    type: "Type",
    define: "Define",
    system: "System",
    version: "Version",
    started: "Started",
    dhcp4Interfaces: "DHCPv4 interfaces",
    dhcp4None: "none (DHCP disabled)",
    dhcp6Interfaces: "DHCPv6 interfaces",
    dhcp6None: "none (DHCPv6 disabled)",
    serverDuid: "DHCPv6 server DUID",
    conflictDetection: "Conflict detection",
    conflictDetectionOn: "active (ICMP)",
    conflictDetectionOff: "off",
  },
  de: {
    title: "Benutzer & Einstellungen",
    tabAccount: "Mein Konto & API-Tokens",
    tabUsers: "Benutzer",
    tabOptions: "Server-Optionen",
    tabBackup: "Sicherung & Import",
    tabSystem: "System",
    passwordChanged: "Passwort geändert; andere Sitzungen wurden beendet",
    changePassword: "Passwort ändern",
    newPasswordTooShort: "Neues Passwort: mindestens 10 Zeichen",
    currentPassword: "Aktuelles Passwort",
    newPassword: "Neues Passwort",
    change: "Ändern",
    apiTokens: "API-Tokens",
    tokenCreated: "Token erstellt – er wird nur jetzt angezeigt:",
    name: "Name",
    prefix: "Präfix",
    role: "Rolle",
    expires: "Läuft ab",
    lastUsed: "Zuletzt benutzt",
    never: "nie",
    revokeToken: "Token „{name}“ widerrufen?",
    revoke: "Widerrufen",
    sameAsUser: "wie Benutzer",
    validDays: "Gültig (Tage)",
    createToken: "Erstellen",
    userCreated: "Benutzer angelegt",
    userUpdated: "Benutzer aktualisiert",
    users: "Benutzer",
    username: "Benutzername",
    status: "Status",
    created: "Angelegt",
    you: "Sie",
    roleOf: "Rolle von {name}",
    setPassword: "Passwort setzen",
    deleteUser: "Benutzer „{name}“ löschen?",
    password: "Passwort",
    minLength: "mind. 10 Zeichen",
    passwordFor: "Passwort für {name}",
    sessionsEnded: "Bestehende Sitzungen des Benutzers werden beendet",
    definitionCreated: "Optionsdefinition angelegt",
    serverOptions: "Server-Optionen",
    serverOptionsHint: "gelten für alle Scopes, sofern nicht überschrieben",
    customDefinitions: "Eigene Optionsdefinitionen",
    code: "Code",
    type: "Typ",
    define: "Definieren",
    system: "System",
    version: "Version",
    started: "Gestartet",
    dhcp4Interfaces: "DHCPv4-Interfaces",
    dhcp4None: "keine (DHCP deaktiviert)",
    dhcp6Interfaces: "DHCPv6-Interfaces",
    dhcp6None: "keine (DHCPv6 deaktiviert)",
    serverDuid: "DHCPv6-Server-DUID",
    conflictDetection: "Konflikterkennung",
    conflictDetectionOn: "aktiv (ICMP)",
    conflictDetectionOff: "aus",
  },
});

type Tab = "account" | "users" | "options" | "backup" | "system";

export function SettingsPage() {
  const { isAdmin } = useAuth();
  const [tab, setTab] = useState<Tab>("account");
  const tabs: { id: Tab; label: string }[] = [
    { id: "account", label: m("tabAccount") },
    ...(isAdmin ? [{ id: "users" as Tab, label: m("tabUsers") }] : []),
    { id: "options", label: m("tabOptions") },
    { id: "backup", label: m("tabBackup") },
    { id: "system", label: m("tabSystem") },
  ];
  return (
    <>
      <PageHeader title={m("title")} />
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
      toast(m("passwordChanged"));
    },
    onError: (e) => setErr(errorMessage(e)),
  });
  return (
    <div className="grid gap-6 lg:grid-cols-2">
      <Card title={m("changePassword")}>
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (next.length < 10) return setErr(m("newPasswordTooShort"));
            pw.mutate();
          }}
        >
          {err && <Alert>{err}</Alert>}
          <Input label={m("currentPassword")} type="password" autoComplete="current-password" value={cur} onChange={(e) => setCur(e.target.value)} />
          <Input label={m("newPassword")} type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} />
          <Button type="submit" variant="primary">
            {m("change")}
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
    <Card title={m("apiTokens")}>
      {created && (
        <Alert kind="success">
          {m("tokenCreated")}
          <code className="mt-1 block break-all">{created}</code>
        </Alert>
      )}
      <Table head={[m("name"), m("prefix"), m("role"), m("expires"), m("lastUsed"), ""]} empty={list.data?.length === 0}>
        {list.data?.map((t) => (
          <tr key={t.id}>
            <Td>{t.name}</Td>
            <Td mono>{t.prefix}…</Td>
            <Td>{roleLabel(t.role)}</Td>
            <Td>{t.expires_at ? formatDateTime(t.expires_at) : m("never")}</Td>
            <Td>{formatDateTime(t.last_used_at)}</Td>
            <Td className="text-right">
              <Button size="sm" variant="ghost" onClick={async () => (await confirm(m("revokeToken", { name: t.name }), { danger: true })) && del.mutate(t.id)}>
                {m("revoke")}
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
        <Input label={m("name")} value={name} onChange={(e) => setName(e.target.value)} required />
        <Select label={m("role")} value={role} onChange={(e) => setRole(e.target.value as S["Role"])}>
          <option value="">{m("sameAsUser")}</option>
          {roles.map((r) => (
            <option key={r} value={r}>
              {roleLabel(r)}
            </option>
          ))}
        </Select>
        <Input label={m("validDays")} type="number" min={1} value={days} onChange={(e) => setDays(e.target.value)} placeholder={c("unlimited")} />
        <Button type="submit" variant="primary">
          {m("createToken")}
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
      toast(m("userCreated"));
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const upd = useMutation({
    mutationFn: (v: { id: number; body: S["UserUpdate"] }) => unwrap(api.PATCH("/users/{id}", { params: { path: { id: v.id } }, body: v.body })),
    onSuccess: () => {
      refresh();
      toast(m("userUpdated"));
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const del = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/users/{id}", { params: { path: { id } } })),
    onSuccess: refresh,
    onError: (e) => toast(errorMessage(e), "error"),
  });
  return (
    <Card title={m("users")}>
      <Table head={[m("username"), m("role"), m("status"), m("created"), ""]}>
        {list.data?.map((u) => (
          <tr key={u.id}>
            <Td className="font-medium">
              {u.username} {u.id === me?.id && <Badge color="teal">{m("you")}</Badge>}
            </Td>
            <Td>
              <select
                aria-label={m("roleOf", { name: u.username })}
                value={u.role}
                onChange={(e) => upd.mutate({ id: u.id, body: { role: e.target.value as S["Role"] } })}
                className="rounded-md border border-slate-300 bg-white px-2 py-1 text-xs dark:border-slate-700 dark:bg-slate-900"
              >
                {(["admin", "operator", "readonly"] as const).map((r) => (
                  <option key={r} value={r}>
                    {roleLabel(r)}
                  </option>
                ))}
              </select>
            </Td>
            <Td>
              <Toggle label={u.disabled ? c("disabled") : c("active")} checked={!u.disabled} onChange={(v) => upd.mutate({ id: u.id, body: { disabled: !v } })} />
            </Td>
            <Td>{formatDateTime(u.created_at)}</Td>
            <Td className="text-right">
              <div className="flex justify-end gap-1">
                <Button size="sm" variant="ghost" onClick={() => setReset(u)}>
                  {m("setPassword")}
                </Button>
                {u.id !== me?.id && (
                  <Button size="sm" variant="ghost" onClick={async () => (await confirm(m("deleteUser", { name: u.username }), { danger: true })) && del.mutate(u.id)}>
                    {c("delete")}
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
        <Input label={m("username")} value={f.username} onChange={(e) => setF({ ...f, username: e.target.value })} required />
        <Input label={m("password")} type="password" autoComplete="new-password" value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} hint={m("minLength")} required />
        <Select label={m("role")} value={f.role} onChange={(e) => setF({ ...f, role: e.target.value as S["Role"] })}>
          {(["readonly", "operator", "admin"] as const).map((r) => (
            <option key={r} value={r}>
              {roleLabel(r)}
            </option>
          ))}
        </Select>
        <Button type="submit" variant="primary">
          {c("create")}
        </Button>
      </form>
      {reset && (
        <Modal
          title={m("passwordFor", { name: reset.username })}
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
              {c("set")}
            </Button>
          }
        >
          <Input label={m("newPassword")} type="password" autoComplete="new-password" value={newPw} onChange={(e) => setNewPw(e.target.value)} hint={m("sessionsEnded")} />
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
      toast(m("definitionCreated"));
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
      <Card title={m("serverOptions")} actions={<span className="text-xs text-slate-500">{m("serverOptionsHint")}</span>}>
        <OptionsEditor owner="server" ownerId={0} />
      </Card>
      <Card title={m("customDefinitions")}>
        <Table head={[m("code"), m("name"), m("type"), ""]} empty={custom.length === 0}>
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
                    {c("delete")}
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
            <Input label={m("code")} type="number" min={1} max={254} value={f.code} onChange={(e) => setF({ ...f, code: e.target.value })} required />
            <Input label={m("name")} value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} required />
            <Select label={m("type")} value={f.type} onChange={(e) => setF({ ...f, type: e.target.value as S["OptionType"] })}>
              {(["ip", "ip-list", "string", "uint8", "uint16", "uint32", "int32", "bool", "hex", "domain-list", "routes"] as const).map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
            </Select>
            <Button type="submit" variant="primary">
              {m("define")}
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
    <Card title={m("system")}>
      {i && (
        <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
          <dt className="text-slate-500">{m("version")}</dt>
          <dd>
            {i.version} ({i.commit})
          </dd>
          <dt className="text-slate-500">Go</dt>
          <dd>{i.go_version}</dd>
          <dt className="text-slate-500">{m("started")}</dt>
          <dd>{formatDateTime(i.started_at)}</dd>
          <dt className="text-slate-500">{m("dhcp4Interfaces")}</dt>
          <dd className="font-mono">{i.dhcp4_interfaces.join(", ") || m("dhcp4None")}</dd>
          <dt className="text-slate-500">{m("dhcp6Interfaces")}</dt>
          <dd className="font-mono">{i.dhcp6_interfaces.join(", ") || m("dhcp6None")}</dd>
          {i.dhcp6_server_duid && (
            <>
              <dt className="text-slate-500">{m("serverDuid")}</dt>
              <dd className="font-mono break-all">{i.dhcp6_server_duid}</dd>
            </>
          )}
          <dt className="text-slate-500">HTTPS</dt>
          <dd>{i.tls ? c("yes") : c("no")}</dd>
          <dt className="text-slate-500">{m("conflictDetection")}</dt>
          <dd>{i.conflict_detection ? m("conflictDetectionOn") : m("conflictDetectionOff")}</dd>
        </dl>
      )}
    </Card>
  );
}

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { Alert, Badge, Button, Card, Input, Modal, PageHeader, Select, Table, Tabs, Td, TextArea, useFeedback } from "../components/ui";
import { messages } from "../i18n";
import { c } from "../i18n/common";
import { formatDateTime } from "../lib/format";

const m = messages({
  en: {
    title: "DNS updates",
    subtitle: "Dynamic registration of A and PTR records (RFC 2136) with TSIG. When records are registered is set per scope on its “DNS” tab.",
    zones: "Zones",
    keys: "TSIG keys",
    registrations: "Registrations",
    zone: "Zone",
    zoneTitle: "Zone {name}",
    addZone: "Add zone",
    zoneDeleted: "Zone deleted",
    zoneSaved: "Zone saved",
    zoneCreated: "Zone created",
    deleteZone: "Delete zone “{name}”?",
    zonesInfo: "The most specific matching zone is used for each name. For PTR records, add the reverse zone as well, e.g.",
    primaryServer: "Primary server",
    serverHint: "IP address or hostname, optionally with :port (default 53)",
    tsigKey: "TSIG key",
    unsigned: "unsigned",
    noKey: "– none (unsigned) –",
    unsignedHint: "Without a key, updates are not authenticated – only for servers that allow updates by IP address.",
    keyTitle: "Key {name}",
    addKey: "Add key",
    keyDeleted: "Key deleted",
    keySaved: "Key saved",
    deleteKey: "Delete key “{name}”?",
    keysInfo:
      "Keys shared with the DNS server (RFC 8945). The secret is no longer shown after saving and is not part of the configuration export; HA partners and backups contain it.",
    adminOnly: "Only administrators can manage keys.",
    name: "Name",
    nameHint: "Must have the same name on the DNS server",
    algorithm: "Algorithm",
    secret: "Secret (Base64)",
    secretKeepHint: "Leave empty to keep the stored secret",
    secretNewHint: "Randomly generated – or paste an existing secret (e.g. from tsig-keygen)",
    regenerate: "Regenerate",
    installTitle: "Add the key to the DNS server",
    secretOnce: "The secret is shown only now.",
    bindPolicy: "BIND: in each zone, also add",
    and: "and",
    pdnsAddress: "with the address of this server.",
    regsTitle: "Registrations of this server",
    regsInfo:
      "What this server has registered in DNS for its leases or is registering right now. Records are removed as soon as the lease ends (if set in the scope). With high availability, each server shows its own registrations.",
    address: "Address",
    status: "Status",
    note: "Note",
    removing: "being removed",
    attempt: "Attempt {n}, next {time}",
    stRegistered: "registered",
    stPending: "pending",
    stConflict: "conflict",
    stError: "error",
  },
  de: {
    title: "DNS-Updates",
    subtitle: "Dynamische Registrierung von A- und PTR-Records (RFC 2136) mit TSIG. Pro Scope wird im Reiter „DNS“ festgelegt, wann registriert wird.",
    zones: "Zonen",
    keys: "TSIG-Schlüssel",
    registrations: "Registrierungen",
    zone: "Zone",
    zoneTitle: "Zone {name}",
    addZone: "Zone hinzufügen",
    zoneDeleted: "Zone gelöscht",
    zoneSaved: "Zone gespeichert",
    zoneCreated: "Zone angelegt",
    deleteZone: "Zone „{name}“ löschen?",
    zonesInfo: "Für jeden Namen wird die spezifischste passende Zone verwendet. Für PTR-Records die Reverse-Zone ebenfalls eintragen, z. B.",
    primaryServer: "Primärer Server",
    serverHint: "IP-Adresse oder Hostname, optional mit :Port (Standard 53)",
    tsigKey: "TSIG-Schlüssel",
    unsigned: "unsigniert",
    noKey: "– ohne (unsigniert) –",
    unsignedHint: "Ohne Schlüssel sind die Updates nicht authentifiziert – nur für Server, die Updates per IP freigeben.",
    keyTitle: "Schlüssel {name}",
    addKey: "Schlüssel hinzufügen",
    keyDeleted: "Schlüssel gelöscht",
    keySaved: "Schlüssel gespeichert",
    deleteKey: "Schlüssel „{name}“ löschen?",
    keysInfo:
      "Gemeinsame Schlüssel mit dem DNS-Server (RFC 8945). Das Geheimnis wird nach dem Speichern nicht mehr angezeigt und ist nicht Teil des Konfigurations-Exports; HA-Partner und Backups enthalten es.",
    adminOnly: "Schlüssel können nur Administratoren verwalten.",
    name: "Name",
    nameHint: "Muss auf dem DNS-Server gleich heißen",
    algorithm: "Algorithmus",
    secret: "Geheimnis (Base64)",
    secretKeepHint: "Leer lassen, um das gespeicherte Geheimnis zu behalten",
    secretNewHint: "Zufällig erzeugt – oder ein vorhandenes Geheimnis einfügen (z. B. aus tsig-keygen)",
    regenerate: "Neu erzeugen",
    installTitle: "Schlüssel auf dem DNS-Server eintragen",
    secretOnce: "Das Geheimnis wird nur jetzt angezeigt.",
    bindPolicy: "BIND: in jeder Zone zusätzlich",
    and: "und",
    pdnsAddress: "mit der Adresse dieses Servers.",
    regsTitle: "Registrierungen dieses Servers",
    regsInfo:
      "Was dieser Server für seine Leases in DNS eingetragen hat oder gerade einträgt. Records werden entfernt, sobald der Lease endet (sofern im Scope eingestellt). Bei Hochverfügbarkeit zeigt jeder Server seine eigenen Registrierungen.",
    address: "Adresse",
    status: "Status",
    note: "Hinweis",
    removing: "wird entfernt",
    attempt: "{n}. Versuch, nächster {time}",
    stRegistered: "registriert",
    stPending: "ausstehend",
    stConflict: "Konflikt",
    stError: "Fehler",
  },
});

type Tab = "zones" | "keys" | "registrations";

export const dnsAlgorithms = ["hmac-sha256", "hmac-sha384", "hmac-sha512", "hmac-sha224", "hmac-sha1", "hmac-md5"] as const;
type Algorithm = (typeof dnsAlgorithms)[number];

export function useDnsZones() {
  return useQuery({ queryKey: ["dns-zones"], queryFn: () => unwrap(api.GET("/dns/zones")) });
}

function useDnsKeys() {
  return useQuery({ queryKey: ["dns-keys"], queryFn: () => unwrap(api.GET("/dns/keys")) });
}

/** Random 256 bit TSIG secret, base64 encoded (like tsig-keygen). */
export function randomSecret(): string {
  const b = new Uint8Array(32);
  crypto.getRandomValues(b);
  return btoa(String.fromCharCode(...b));
}

export function DNSPage() {
  const [tab, setTab] = useState<Tab>("zones");
  return (
    <>
      <PageHeader title={m("title")} subtitle={m("subtitle")} />
      <Tabs<Tab>
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "zones", label: m("zones") },
          { id: "keys", label: m("keys") },
          { id: "registrations", label: m("registrations") },
        ]}
      />
      {tab === "zones" && <Zones />}
      {tab === "keys" && <Keys />}
      {tab === "registrations" && <Registrations />}
    </>
  );
}

// ---- zones -------------------------------------------------------------------------

function Zones() {
  const { canWrite } = useAuth();
  const zones = useDnsZones();
  const qc = useQueryClient();
  const { toast, confirm } = useFeedback();
  const [edit, setEdit] = useState<S["DnsZone"] | "new" | null>(null);
  const del = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/dns/zones/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["dns-zones"] });
      toast(m("zoneDeleted"));
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  return (
    <Card
      title={m("zones")}
      actions={
        canWrite && (
          <Button variant="primary" size="sm" onClick={() => setEdit("new")}>
            {m("addZone")}
          </Button>
        )
      }
    >
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        {m("zonesInfo")} <code>1.168.192.in-addr.arpa</code>.
      </p>
      <Table head={[m("zone"), m("primaryServer"), m("tsigKey"), ""]} empty={zones.data?.length === 0}>
        {zones.data?.map((z) => (
          <tr key={z.id}>
            <Td mono>{z.name}</Td>
            <Td mono>{z.server}</Td>
            <Td>{z.key_name ? <span className="font-mono">{z.key_name}</span> : <Badge color="amber">{m("unsigned")}</Badge>}</Td>
            <Td className="text-right whitespace-nowrap">
              {canWrite && (
                <>
                  <Button size="sm" variant="ghost" onClick={() => setEdit(z)}>
                    {c("edit")}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={async () => (await confirm(m("deleteZone", { name: z.name }), { danger: true, confirmLabel: c("delete") })) && del.mutate(z.id)}
                  >
                    {c("delete")}
                  </Button>
                </>
              )}
            </Td>
          </tr>
        ))}
      </Table>
      {edit && <ZoneForm zone={edit === "new" ? null : edit} onClose={() => setEdit(null)} />}
    </Card>
  );
}

function ZoneForm({ zone, onClose }: { zone: S["DnsZone"] | null; onClose: () => void }) {
  const keys = useDnsKeys();
  const qc = useQueryClient();
  const { toast } = useFeedback();
  const [f, setF] = useState({ name: zone?.name ?? "", server: zone?.server ?? "", key_id: zone?.key_id ?? 0 });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const save = useMutation({
    mutationFn: () =>
      zone
        ? unwrap(api.PUT("/dns/zones/{id}", { params: { path: { id: zone.id } }, body: f }))
        : unwrap(api.POST("/dns/zones", { body: f })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["dns-zones"] });
      toast(zone ? m("zoneSaved") : m("zoneCreated"));
      onClose();
    },
    onError: (e) => {
      if (e instanceof ApiError) setErrors(e.fields);
      toast(errorMessage(e), "error");
    },
  });
  return (
    <Modal
      title={zone ? m("zoneTitle", { name: zone.name }) : m("addZone")}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{c("cancel")}</Button>
          <Button variant="primary" onClick={() => save.mutate()} disabled={save.isPending || !f.name || !f.server}>
            {c("save")}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Input label={m("zone")} value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder="example.lan" error={errors.name} autoFocus />
        <Input
          label={m("primaryServer")}
          value={f.server}
          onChange={(e) => setF({ ...f, server: e.target.value })}
          placeholder="192.168.1.53"
          hint={m("serverHint")}
          error={errors.server}
        />
        <Select
          label={m("tsigKey")}
          value={String(f.key_id)}
          onChange={(e) => setF({ ...f, key_id: Number(e.target.value) })}
          error={errors.key_id}
          hint={f.key_id === 0 ? m("unsignedHint") : undefined}
        >
          <option value="0">{m("noKey")}</option>
          {keys.data?.map((k) => (
            <option key={k.id} value={k.id}>
              {k.name} ({k.algorithm})
            </option>
          ))}
        </Select>
      </div>
    </Modal>
  );
}

// ---- TSIG keys ---------------------------------------------------------------------

function Keys() {
  const { isAdmin, configReadOnly } = useAuth();
  const canEdit = isAdmin && !configReadOnly;
  const keys = useDnsKeys();
  const qc = useQueryClient();
  const { toast, confirm } = useFeedback();
  const [edit, setEdit] = useState<S["DnsKey"] | "new" | null>(null);
  const del = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/dns/keys/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["dns-keys"] });
      toast(m("keyDeleted"));
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  return (
    <Card
      title={m("keys")}
      actions={
        canEdit && (
          <Button variant="primary" size="sm" onClick={() => setEdit("new")}>
            {m("addKey")}
          </Button>
        )
      }
    >
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">{m("keysInfo")}</p>
      {!isAdmin && <Alert kind="info">{m("adminOnly")}</Alert>}
      <Table head={[m("name"), m("algorithm"), ""]} empty={keys.data?.length === 0}>
        {keys.data?.map((k) => (
          <tr key={k.id}>
            <Td mono>{k.name}</Td>
            <Td mono>{k.algorithm}</Td>
            <Td className="text-right whitespace-nowrap">
              {canEdit && (
                <>
                  <Button size="sm" variant="ghost" onClick={() => setEdit(k)}>
                    {c("edit")}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={async () => (await confirm(m("deleteKey", { name: k.name }), { danger: true, confirmLabel: c("delete") })) && del.mutate(k.id)}
                  >
                    {c("delete")}
                  </Button>
                </>
              )}
            </Td>
          </tr>
        ))}
      </Table>
      {edit && <KeyForm dnsKey={edit === "new" ? null : edit} onClose={() => setEdit(null)} />}
    </Card>
  );
}

function KeyForm({ dnsKey, onClose }: { dnsKey: S["DnsKey"] | null; onClose: () => void }) {
  const qc = useQueryClient();
  const { toast } = useFeedback();
  const [f, setF] = useState({
    name: dnsKey?.name ?? "leasorix-key",
    algorithm: (dnsKey?.algorithm ?? "hmac-sha256") as Algorithm,
    secret: dnsKey ? "" : randomSecret(),
  });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [saved, setSaved] = useState<{ name: string; algorithm: string; secret: string } | null>(null);
  const save = useMutation({
    mutationFn: () => {
      const body = { name: f.name, algorithm: f.algorithm, secret: f.secret };
      return dnsKey ? unwrap(api.PUT("/dns/keys/{id}", { params: { path: { id: dnsKey.id } }, body })) : unwrap(api.POST("/dns/keys", { body }));
    },
    onSuccess: (k) => {
      qc.invalidateQueries({ queryKey: ["dns-keys"] });
      qc.invalidateQueries({ queryKey: ["dns-zones"] });
      toast(m("keySaved"));
      if (f.secret) setSaved({ name: k.name, algorithm: k.algorithm, secret: f.secret });
      else onClose();
    },
    onError: (e) => {
      if (e instanceof ApiError) setErrors(e.fields);
      toast(errorMessage(e), "error");
    },
  });
  if (saved) {
    return (
      <Modal title={m("installTitle")} onClose={onClose} footer={<Button onClick={onClose}>{c("done")}</Button>} wide>
        <div className="space-y-4 text-sm">
          <Alert kind="info">{m("secretOnce")}</Alert>
          <TextArea label="BIND (named.conf)" rows={4} readOnly value={bindSnippet(saved)} />
          <TextArea label="PowerDNS (pdnsutil)" rows={3} readOnly value={pdnsSnippet(saved)} />
          <p className="text-slate-600 dark:text-slate-400">
            {m("bindPolicy")} <code>update-policy {"{"} grant {saved.name} zonesub ANY; {"}"};</code>. PowerDNS: <code>dnsupdate=yes</code> {m("and")}{" "}
            <code>allow-dnsupdate-from</code> {m("pdnsAddress")}
          </p>
        </div>
      </Modal>
    );
  }
  return (
    <Modal
      title={dnsKey ? m("keyTitle", { name: dnsKey.name }) : m("addKey")}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{c("cancel")}</Button>
          <Button variant="primary" onClick={() => save.mutate()} disabled={save.isPending || !f.name || (!dnsKey && !f.secret)}>
            {c("save")}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Input label={m("name")} value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} error={errors.name} hint={m("nameHint")} />
        <Select label={m("algorithm")} value={f.algorithm} onChange={(e) => setF({ ...f, algorithm: e.target.value as Algorithm })} error={errors.algorithm}>
          {dnsAlgorithms.map((a) => (
            <option key={a} value={a}>
              {a}
            </option>
          ))}
        </Select>
        <div>
          <TextArea
            label={m("secret")}
            rows={2}
            value={f.secret}
            onChange={(e) => setF({ ...f, secret: e.target.value.trim() })}
            error={errors.secret}
            hint={dnsKey ? m("secretKeepHint") : m("secretNewHint")}
          />
          <Button size="sm" variant="ghost" className="mt-1" onClick={() => setF({ ...f, secret: randomSecret() })}>
            {m("regenerate")}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function bindSnippet(k: { name: string; algorithm: string; secret: string }) {
  return `key "${k.name}" {\n    algorithm ${k.algorithm};\n    secret "${k.secret}";\n};`;
}

function pdnsSnippet(k: { name: string; algorithm: string; secret: string }) {
  return `pdnsutil import-tsig-key ${k.name} ${k.algorithm} ${k.secret}\npdnsutil set-meta <zone> TSIG-ALLOW-DNSUPDATE ${k.name}`;
}

// ---- registrations -------------------------------------------------------------------

// Labels are message keys, resolved when rendering.
const stateBadge: Record<S["DnsRegistration"]["state"], { color: "green" | "blue" | "amber" | "red"; label: "stRegistered" | "stPending" | "stConflict" | "stError" }> = {
  registered: { color: "green", label: "stRegistered" },
  pending: { color: "blue", label: "stPending" },
  conflict: { color: "amber", label: "stConflict" },
  error: { color: "red", label: "stError" },
};

function Registrations() {
  const regs = useQuery({ queryKey: ["dns-registrations"], queryFn: () => unwrap(api.GET("/dns/registrations")), refetchInterval: 5000 });
  return (
    <Card title={m("regsTitle")}>
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">{m("regsInfo")}</p>
      <Table head={[m("address"), m("name"), "A", "PTR", m("status"), m("note")]} empty={regs.data?.length === 0}>
        {regs.data?.map((r) => {
          const b = stateBadge[r.state];
          return (
            <tr key={r.ip}>
              <Td mono>{r.ip}</Td>
              <Td mono>{r.fqdn || <span className="text-slate-500">{m("removing")}</span>}</Td>
              <Td mono>{r.a_record || "–"}</Td>
              <Td mono>{r.ptr_record || "–"}</Td>
              <Td>
                <Badge color={b.color}>{m(b.label)}</Badge>
              </Td>
              <Td className="max-w-md text-xs">
                {r.last_error}
                {r.state === "error" && r.next_attempt && (
                  <span className="block text-slate-500">{m("attempt", { n: r.attempts, time: formatDateTime(r.next_attempt) })}</span>
                )}
              </Td>
            </tr>
          );
        })}
      </Table>
    </Card>
  );
}

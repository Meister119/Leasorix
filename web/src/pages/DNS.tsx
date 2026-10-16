import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { Alert, Badge, Button, Card, Input, Modal, PageHeader, Select, Table, Tabs, Td, TextArea, useFeedback } from "../components/ui";
import { formatDateTime } from "../lib/format";

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
      <PageHeader
        title="DNS-Updates"
        subtitle="Dynamische Registrierung von A- und PTR-Records (RFC 2136) mit TSIG. Pro Scope wird unter „Einstellungen → DNS“ festgelegt, wann registriert wird."
      />
      <Tabs<Tab>
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "zones", label: "Zonen" },
          { id: "keys", label: "TSIG-Schlüssel" },
          { id: "registrations", label: "Registrierungen" },
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
      toast("Zone gelöscht");
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  return (
    <Card
      title="Zonen"
      actions={
        canWrite && (
          <Button variant="primary" size="sm" onClick={() => setEdit("new")}>
            Zone hinzufügen
          </Button>
        )
      }
    >
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        Für jeden Namen wird die spezifischste passende Zone verwendet. Für PTR-Records die Reverse-Zone ebenfalls eintragen, z. B.{" "}
        <code>1.168.192.in-addr.arpa</code>.
      </p>
      <Table head={["Zone", "Primärer Server", "TSIG-Schlüssel", ""]} empty={zones.data?.length === 0}>
        {zones.data?.map((z) => (
          <tr key={z.id}>
            <Td mono>{z.name}</Td>
            <Td mono>{z.server}</Td>
            <Td>{z.key_name ? <span className="font-mono">{z.key_name}</span> : <Badge color="amber">unsigniert</Badge>}</Td>
            <Td className="text-right whitespace-nowrap">
              {canWrite && (
                <>
                  <Button size="sm" variant="ghost" onClick={() => setEdit(z)}>
                    Bearbeiten
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={async () => (await confirm(`Zone „${z.name}“ löschen?`, { danger: true, confirmLabel: "Löschen" })) && del.mutate(z.id)}
                  >
                    Löschen
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
      toast(zone ? "Zone gespeichert" : "Zone angelegt");
      onClose();
    },
    onError: (e) => {
      if (e instanceof ApiError) setErrors(e.fields);
      toast(errorMessage(e), "error");
    },
  });
  return (
    <Modal
      title={zone ? `Zone ${zone.name}` : "Zone hinzufügen"}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>Abbrechen</Button>
          <Button variant="primary" onClick={() => save.mutate()} disabled={save.isPending || !f.name || !f.server}>
            Speichern
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Input label="Zone" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder="example.lan" error={errors.name} autoFocus />
        <Input
          label="Primärer Server"
          value={f.server}
          onChange={(e) => setF({ ...f, server: e.target.value })}
          placeholder="192.168.1.53"
          hint="IP-Adresse oder Hostname, optional mit :Port (Standard 53)"
          error={errors.server}
        />
        <Select
          label="TSIG-Schlüssel"
          value={String(f.key_id)}
          onChange={(e) => setF({ ...f, key_id: Number(e.target.value) })}
          error={errors.key_id}
          hint={f.key_id === 0 ? "Ohne Schlüssel sind die Updates nicht authentifiziert – nur für Server, die Updates per IP freigeben." : undefined}
        >
          <option value="0">– ohne (unsigniert) –</option>
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
      toast("Schlüssel gelöscht");
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  return (
    <Card
      title="TSIG-Schlüssel"
      actions={
        canEdit && (
          <Button variant="primary" size="sm" onClick={() => setEdit("new")}>
            Schlüssel hinzufügen
          </Button>
        )
      }
    >
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        Gemeinsame Schlüssel mit dem DNS-Server (RFC 8945). Das Geheimnis wird nach dem Speichern nicht mehr angezeigt und ist nicht Teil des
        Konfigurations-Exports; HA-Partner und Backups enthalten es.
      </p>
      {!isAdmin && <Alert kind="info">Schlüssel können nur Administratoren verwalten.</Alert>}
      <Table head={["Name", "Algorithmus", ""]} empty={keys.data?.length === 0}>
        {keys.data?.map((k) => (
          <tr key={k.id}>
            <Td mono>{k.name}</Td>
            <Td mono>{k.algorithm}</Td>
            <Td className="text-right whitespace-nowrap">
              {canEdit && (
                <>
                  <Button size="sm" variant="ghost" onClick={() => setEdit(k)}>
                    Bearbeiten
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={async () => (await confirm(`Schlüssel „${k.name}“ löschen?`, { danger: true, confirmLabel: "Löschen" })) && del.mutate(k.id)}
                  >
                    Löschen
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
      toast("Schlüssel gespeichert");
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
      <Modal title="Schlüssel auf dem DNS-Server eintragen" onClose={onClose} footer={<Button onClick={onClose}>Fertig</Button>} wide>
        <div className="space-y-4 text-sm">
          <Alert kind="info">Das Geheimnis wird nur jetzt angezeigt.</Alert>
          <TextArea label="BIND (named.conf)" rows={4} readOnly value={bindSnippet(saved)} />
          <TextArea label="PowerDNS (pdnsutil)" rows={3} readOnly value={pdnsSnippet(saved)} />
          <p className="text-slate-600 dark:text-slate-400">
            BIND: in jeder Zone zusätzlich <code>update-policy {"{"} grant {saved.name} zonesub ANY; {"}"};</code>. PowerDNS:{" "}
            <code>dnsupdate=yes</code> und <code>allow-dnsupdate-from</code> mit der Adresse dieses Servers.
          </p>
        </div>
      </Modal>
    );
  }
  return (
    <Modal
      title={dnsKey ? `Schlüssel ${dnsKey.name}` : "Schlüssel hinzufügen"}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>Abbrechen</Button>
          <Button variant="primary" onClick={() => save.mutate()} disabled={save.isPending || !f.name || (!dnsKey && !f.secret)}>
            Speichern
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Input label="Name" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} error={errors.name} hint="Muss auf dem DNS-Server gleich heißen" />
        <Select label="Algorithmus" value={f.algorithm} onChange={(e) => setF({ ...f, algorithm: e.target.value as Algorithm })} error={errors.algorithm}>
          {dnsAlgorithms.map((a) => (
            <option key={a} value={a}>
              {a}
            </option>
          ))}
        </Select>
        <div>
          <TextArea
            label="Geheimnis (Base64)"
            rows={2}
            value={f.secret}
            onChange={(e) => setF({ ...f, secret: e.target.value.trim() })}
            error={errors.secret}
            hint={dnsKey ? "Leer lassen, um das gespeicherte Geheimnis zu behalten" : "Zufällig erzeugt – oder ein vorhandenes Geheimnis einfügen (z. B. aus tsig-keygen)"}
          />
          <Button size="sm" variant="ghost" className="mt-1" onClick={() => setF({ ...f, secret: randomSecret() })}>
            Neu erzeugen
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

const stateBadge: Record<S["DnsRegistration"]["state"], { color: "green" | "blue" | "amber" | "red"; label: string }> = {
  registered: { color: "green", label: "registriert" },
  pending: { color: "blue", label: "ausstehend" },
  conflict: { color: "amber", label: "Konflikt" },
  error: { color: "red", label: "Fehler" },
};

function Registrations() {
  const regs = useQuery({ queryKey: ["dns-registrations"], queryFn: () => unwrap(api.GET("/dns/registrations")), refetchInterval: 5000 });
  return (
    <Card title="Registrierungen dieses Servers">
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        Was dieser Server für seine Leases in DNS eingetragen hat oder gerade einträgt. Records werden entfernt, sobald der Lease endet (sofern
        im Scope eingestellt). Bei Hochverfügbarkeit zeigt jeder Server seine eigenen Registrierungen.
      </p>
      <Table head={["Adresse", "Name", "A", "PTR", "Status", "Hinweis"]} empty={regs.data?.length === 0}>
        {regs.data?.map((r) => {
          const b = stateBadge[r.state];
          return (
            <tr key={r.ip}>
              <Td mono>{r.ip}</Td>
              <Td mono>{r.fqdn || <span className="text-slate-500">wird entfernt</span>}</Td>
              <Td mono>{r.a_record || "–"}</Td>
              <Td mono>{r.ptr_record || "–"}</Td>
              <Td>
                <Badge color={b.color}>{b.label}</Badge>
              </Td>
              <Td className="max-w-md text-xs">
                {r.last_error}
                {r.state === "error" && r.next_attempt && (
                  <span className="block text-slate-500">
                    {r.attempts}. Versuch, nächster {formatDateTime(r.next_attempt)}
                  </span>
                )}
              </Td>
            </tr>
          );
        })}
      </Table>
    </Card>
  );
}

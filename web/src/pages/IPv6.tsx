import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "react-router";
import { api, ApiError, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { Alert, Badge, Button, Card, Input, Modal, PageHeader, Select, Spinner, Table, Tabs, Td, TextArea, Toggle, useFeedback } from "../components/ui";
import { messages } from "../i18n";
import { c } from "../i18n/common";
import { formatDateTime, formatDuration, relativeTime, stateLabel } from "../lib/format";
import { hasErrors, type Errors } from "../lib/validate";

const m = messages({
  en: {
    title: "IPv6 (DHCPv6)",
    subtitle: "Stateful DHCPv6: addresses (IA_NA) and prefix delegation (IA_PD)",
    newScope: "New IPv6 scope",
    inactive1: "DHCPv6 is not active on this instance (no interfaces configured under",
    inactive2: "). You can still edit the configuration.",
    scopes: "Scopes",
    reservations: "Reservations",
    leases: "Leases",
    serverOptions: "Server options",
    serverOptionsTitle: "Server options (DHCPv6)",
    serverOptionsHint: "apply to all IPv6 scopes",
    scopeDeleted: "IPv6 scope deleted",
    scopeCreated: "IPv6 scope “{name}” created",
    scopeSaved: "IPv6 scope saved",
    deleteScope: "Delete IPv6 scope “{name}” including its reservations, options and leases?",
    name: "Name",
    prefix: "Prefix",
    addressRange: "Address range",
    prefixDelegation: "Prefix delegation",
    lifetime: "Lifetime",
    status: "Status",
    inUse: "In use",
    usedAddresses: "{n} addr.",
    usedPrefixes: "{n} pref.",
    nameRequired: "Enter a name",
    prefixInvalid: "IPv6 prefix, e.g. 2001:db8:1::/64",
    startEnd: "Enter start and end or leave both empty",
    rangeOrPd: "Enter an address range and/or prefix delegation",
    validMin: "at least 60 seconds",
    preferredInvalid: "positive and not longer than the valid lifetime",
    namePlaceholder: "e.g. Office LAN v6",
    prefixLink: "Prefix (link)",
    prefixFixed: "The prefix of a scope cannot be changed",
    prefixHint: "Prefix of the network the clients are on",
    startAddress: "Start address",
    startHint: "empty = no address assignment",
    endAddress: "End address",
    preferredLifetime: "Preferred lifetime (s)",
    validLifetime: "Valid lifetime (s)",
    pdPool: "Delegation pool",
    pdPoolHint: "empty = no prefix delegation",
    pdLength: "Delegated prefix length",
    pdLengthHint: "e.g. 56 for a /56 per router",
    preference: "Server preference (0–255)",
    preferenceHint: "higher = preferred; 255 = accept immediately",
    rapidCommit: "Rapid Commit (two-message exchange)",
    scopeActive: "Scope active",
    description: "Description",
    createScope: "Create scope",
    createNote: "DNS servers (option 23) and the domain search list (option 24) can be set under “Options” after the scope has been created.",
    overview: "Overview",
    exclusions: "Exclusions",
    options: "Options",
    scopeOptions: "Scope options",
    scopeOptionsHint: "override server options",
    statistics: "Statistics",
    addressesInRange: "Addresses in range",
    assignedAddresses: "Assigned addresses",
    declined: "Blocked (decline)",
    prefixCapacity: "Delegable prefixes",
    delegatedPrefixes: "Delegated prefixes",
    settings: "Settings",
    exclusionAdded: "Exclusion added",
    exclusionRanges: "Exclusion ranges",
    exclusionRangesHint: "Addresses are not assigned dynamically",
    from: "From",
    to: "To",
    addressRequired: "Enter an address",
    toHint: "empty = single address only",
    duidInvalid: "DUID in hex, e.g. 00:03:00:01:02:00:00:00:00:01",
    iaidInvalid: "Number or empty (any IA)",
    ipRequired: "Enter an IPv6 address",
    scopeRequired: "Select a scope",
    reservationCreated: "Reservation created",
    reservationDeleted: "Reservation deleted",
    reservationSaved: "Reservation saved",
    newReservation: "New reservation",
    any: "any",
    ipv6Address: "IPv6 address",
    hostname: "Hostname",
    scope: "Scope",
    scopePlaceholder: "– Scope –",
    deleteReservation: "Delete reservation {ip}?",
    reservationTitle: "Reservation {ip}",
    iaidHint: "empty = any IA of the client",
    reservationOptions: "Reservation options (highest priority)",
    hintIpv6List: "separate multiple addresses with commas, e.g. 2001:db8::53",
    hintDomainList: "e.g. example.com, corp.example.com",
    hintString: "text",
    hintHex: "e.g. 01:0a:ff",
    hintNtpServers: "addresses or names, separated by commas",
    optionSaved: "Option saved",
    optionRemoved: "Option removed",
    code: "Code",
    type: "Type",
    value: "Value",
    removeOption: "Remove option {code} ({name})?",
    selectOption: "Select an option",
    codeRange: "Enter a code from 1 to 65535",
    option: "Option",
    otherOption: "other (enter code and type)",
    leaseDeleted: "Lease deleted",
    searchHint: "Address, DUID, hostname",
    addressPrefix: "Address / prefix",
    kind: "Kind",
    state: "State",
    expires: "Expires",
    kindPrefix: "Prefix",
    kindAddress: "Address",
    lifetimes: "preferred until {preferred}, valid until {valid}",
    deleteLeasePrefix: "Delete lease {label}? The prefix becomes available again immediately.",
    deleteLeaseAddress: "Delete lease {label}? The address becomes available again immediately.",
  },
  de: {
    title: "IPv6 (DHCPv6)",
    subtitle: "Stateful DHCPv6: Adressen (IA_NA) und Präfix-Delegation (IA_PD)",
    newScope: "Neuer IPv6-Scope",
    inactive1: "DHCPv6 ist auf dieser Instanz nicht aktiv (keine Interfaces unter",
    inactive2: " konfiguriert). Die Konfiguration lässt sich trotzdem pflegen.",
    scopes: "Scopes",
    reservations: "Reservierungen",
    leases: "Leases",
    serverOptions: "Server-Optionen",
    serverOptionsTitle: "Server-Optionen (DHCPv6)",
    serverOptionsHint: "gelten für alle IPv6-Scopes",
    scopeDeleted: "IPv6-Scope gelöscht",
    scopeCreated: "IPv6-Scope „{name}“ angelegt",
    scopeSaved: "IPv6-Scope gespeichert",
    deleteScope: "IPv6-Scope „{name}“ samt Reservierungen, Optionen und Leases löschen?",
    name: "Name",
    prefix: "Präfix",
    addressRange: "Adressbereich",
    prefixDelegation: "Präfix-Delegation",
    lifetime: "Lebensdauer",
    status: "Status",
    inUse: "Belegt",
    usedAddresses: "{n} Adr.",
    usedPrefixes: "{n} Präf.",
    nameRequired: "Name angeben",
    prefixInvalid: "IPv6-Präfix, z. B. 2001:db8:1::/64",
    startEnd: "Start und Ende angeben oder beide leer lassen",
    rangeOrPd: "Adressbereich und/oder Präfix-Delegation angeben",
    validMin: "mindestens 60 Sekunden",
    preferredInvalid: "positiv und nicht länger als die gültige Lebensdauer",
    namePlaceholder: "z. B. Büro-LAN v6",
    prefixLink: "Präfix (Link)",
    prefixFixed: "Das Präfix eines Scopes ist unveränderlich",
    prefixHint: "Präfix des Netzes, in dem die Clients stehen",
    startAddress: "Startadresse",
    startHint: "leer = keine Adressvergabe",
    endAddress: "Endadresse",
    preferredLifetime: "Bevorzugte Lebensdauer (s)",
    validLifetime: "Gültige Lebensdauer (s)",
    pdPool: "Delegations-Pool",
    pdPoolHint: "leer = keine Präfix-Delegation",
    pdLength: "Länge delegierter Präfixe",
    pdLengthHint: "z. B. 56 für /56 je Router",
    preference: "Server-Präferenz (0–255)",
    preferenceHint: "höher = bevorzugt; 255 = sofort annehmen",
    rapidCommit: "Rapid Commit (2-Nachrichten-Austausch)",
    scopeActive: "Scope aktiv",
    description: "Beschreibung",
    createScope: "Scope anlegen",
    createNote: "DNS-Server (Option 23) und Domänen-Suchliste (Option 24) lassen sich nach dem Anlegen unter „Optionen“ festlegen.",
    overview: "Übersicht",
    exclusions: "Ausschlüsse",
    options: "Optionen",
    scopeOptions: "Scope-Optionen",
    scopeOptionsHint: "überschreiben Server-Optionen",
    statistics: "Statistik",
    addressesInRange: "Adressen im Bereich",
    assignedAddresses: "vergebene Adressen",
    declined: "gesperrt (Decline)",
    prefixCapacity: "delegierbare Präfixe",
    delegatedPrefixes: "delegierte Präfixe",
    settings: "Einstellungen",
    exclusionAdded: "Ausschluss angelegt",
    exclusionRanges: "Ausschlussbereiche",
    exclusionRangesHint: "Adressen werden nicht dynamisch vergeben",
    from: "Von",
    to: "Bis",
    addressRequired: "Adresse angeben",
    toHint: "leer = nur eine Adresse",
    duidInvalid: "DUID als Hex, z. B. 00:03:00:01:02:00:00:00:00:01",
    iaidInvalid: "Zahl oder leer (beliebige IA)",
    ipRequired: "IPv6-Adresse angeben",
    scopeRequired: "Scope wählen",
    reservationCreated: "Reservierung angelegt",
    reservationDeleted: "Reservierung gelöscht",
    reservationSaved: "Reservierung gespeichert",
    newReservation: "Neue Reservierung",
    any: "beliebig",
    ipv6Address: "IPv6-Adresse",
    hostname: "Hostname",
    scope: "Scope",
    scopePlaceholder: "– Scope –",
    deleteReservation: "Reservierung {ip} löschen?",
    reservationTitle: "Reservierung {ip}",
    iaidHint: "leer = beliebige IA des Clients",
    reservationOptions: "Reservierungs-Optionen (höchste Priorität)",
    hintIpv6List: "mehrere Adressen mit Komma trennen, z. B. 2001:db8::53",
    hintDomainList: "z. B. example.com, corp.example.com",
    hintString: "Text",
    hintHex: "z. B. 01:0a:ff",
    hintNtpServers: "Adressen oder Namen, mit Komma getrennt",
    optionSaved: "Option gespeichert",
    optionRemoved: "Option entfernt",
    code: "Code",
    type: "Typ",
    value: "Wert",
    removeOption: "Option {code} ({name}) entfernen?",
    selectOption: "Option auswählen",
    codeRange: "Code 1–65535 angeben",
    option: "Option",
    otherOption: "andere (Code und Typ angeben)",
    leaseDeleted: "Lease gelöscht",
    searchHint: "Adresse, DUID, Hostname",
    addressPrefix: "Adresse / Präfix",
    kind: "Art",
    state: "Zustand",
    expires: "Läuft ab",
    kindPrefix: "Präfix",
    kindAddress: "Adresse",
    lifetimes: "bevorzugt bis {preferred}, gültig bis {valid}",
    deleteLeasePrefix: "Lease {label} löschen? Das Präfix wird sofort wieder frei.",
    deleteLeaseAddress: "Lease {label} löschen? Die Adresse wird sofort wieder frei.",
  },
});

export function useScopes6() {
  return useQuery({ queryKey: ["scopes6"], queryFn: () => unwrap(api.GET("/v6/scopes")) });
}

function useStats6() {
  return useQuery({ queryKey: ["stats6"], queryFn: () => unwrap(api.GET("/v6/stats")), refetchInterval: 10000 });
}

type PageTab = "scopes" | "reservations" | "leases" | "options";

export function IPv6Page() {
  const { canWrite } = useAuth();
  const [tab, setTab] = useState<PageTab>("scopes");
  const [create, setCreate] = useState(false);
  const info = useQuery({ queryKey: ["system"], queryFn: () => unwrap(api.GET("/system")) });
  return (
    <>
      <PageHeader
        title={m("title")}
        subtitle={m("subtitle")}
        actions={
          canWrite &&
          tab === "scopes" && (
            <Button variant="primary" onClick={() => setCreate(true)}>
              {m("newScope")}
            </Button>
          )
        }
      />
      {info.data && info.data.dhcp6_interfaces.length === 0 && (
        <div className="mb-4">
          <Alert kind="info">
            {m("inactive1")} <code>dhcp6.interfaces</code>
            {m("inactive2")}
          </Alert>
        </div>
      )}
      <Tabs<PageTab>
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "scopes", label: m("scopes") },
          { id: "reservations", label: m("reservations") },
          { id: "leases", label: m("leases") },
          { id: "options", label: m("serverOptions") },
        ]}
      />
      {tab === "scopes" && <Scopes6Table />}
      {tab === "reservations" && <Reservations6Table scopeId={0} />}
      {tab === "leases" && <Leases6Table scopeId={0} />}
      {tab === "options" && (
        <Card title={m("serverOptionsTitle")} actions={<span className="text-xs text-slate-500">{m("serverOptionsHint")}</span>}>
          <Options6Editor owner="server" ownerId={0} />
        </Card>
      )}
      {create && <CreateScope6 onClose={() => setCreate(false)} />}
    </>
  );
}

function Scopes6Table() {
  const { canWrite } = useAuth();
  const scopes = useScopes6();
  const stats = useStats6();
  const qc = useQueryClient();
  const { toast, confirm } = useFeedback();
  const byId = useMemo(() => new Map(stats.data?.map((s) => [s.scope_id, s])), [stats.data]);
  const del = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/v6/scopes/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["scopes6"] });
      qc.invalidateQueries({ queryKey: ["stats6"] });
      toast(m("scopeDeleted"));
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  if (scopes.isLoading) return <Spinner />;
  return (
    <div className="rounded-lg border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
      <Table
        head={[m("name"), m("prefix"), m("addressRange"), m("prefixDelegation"), m("lifetime"), m("status"), m("inUse"), ""]}
        empty={scopes.data?.length === 0}
      >
        {scopes.data?.map((s) => {
          const st = byId.get(s.id);
          return (
            <tr key={s.id}>
              <Td>
                <Link to={`/ipv6/scopes/${s.id}`} className="font-medium text-teal-700 hover:underline dark:text-teal-400">
                  {s.name}
                </Link>
                {s.rapid_commit && (
                  <span className="ml-2">
                    <Badge color="blue">Rapid Commit</Badge>
                  </span>
                )}
              </Td>
              <Td mono>{s.prefix}</Td>
              <Td mono>{s.start ? `${s.start} – ${s.end}` : "–"}</Td>
              <Td mono>{s.pd_pool ? `${s.pd_pool} → /${s.pd_length}` : "–"}</Td>
              <Td>
                {formatDuration(s.preferred_lifetime)} / {formatDuration(s.valid_lifetime)}
              </Td>
              <Td>{s.enabled ? <Badge color="green">{c("active")}</Badge> : <Badge color="amber">{c("disabled")}</Badge>}</Td>
              <Td className="tabular-nums">
                {st ? (
                  <>
                    {m("usedAddresses", { n: st.addresses_in_use })}
                    {s.pd_pool && ` · ${m("usedPrefixes", { n: st.prefixes_in_use })}`}
                  </>
                ) : (
                  "–"
                )}
              </Td>
              <Td className="text-right">
                {canWrite && (
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={async () => (await confirm(m("deleteScope", { name: s.name }), { danger: true, confirmLabel: c("delete") })) && del.mutate(s.id)}
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
  );
}

interface Scope6Form {
  name: string;
  description: string;
  prefix: string;
  start: string;
  end: string;
  preferred_lifetime: number;
  valid_lifetime: number;
  rapid_commit: boolean;
  preference: number;
  enabled: boolean;
  pd_pool: string;
  pd_length: number;
}

const emptyScope6: Scope6Form = {
  name: "",
  description: "",
  prefix: "",
  start: "",
  end: "",
  preferred_lifetime: 3600,
  valid_lifetime: 7200,
  rapid_commit: false,
  preference: 0,
  enabled: true,
  pd_pool: "",
  pd_length: 56,
};

/** Light client-side checks; the server validates everything again. */
function validateScope6(f: Scope6Form): Errors {
  const e: Errors = {};
  if (!f.name.trim()) e.name = m("nameRequired");
  if (!/^[0-9a-fA-F:.]+\/\d{1,3}$/.test(f.prefix.trim())) e.prefix = m("prefixInvalid");
  if (!!f.start.trim() !== !!f.end.trim()) e.end = m("startEnd");
  if (!f.start.trim() && !f.pd_pool.trim()) e.start = m("rangeOrPd");
  if (!(f.valid_lifetime >= 60)) e.valid_lifetime = m("validMin");
  if (!(f.preferred_lifetime > 0) || f.preferred_lifetime > f.valid_lifetime) e.preferred_lifetime = m("preferredInvalid");
  if (f.preference < 0 || f.preference > 255) e.preference = "0–255";
  return e;
}

function scope6Body(f: Scope6Form): S["Scope6Input"] {
  const pd = f.pd_pool.trim();
  return {
    name: f.name.trim(),
    description: f.description,
    prefix: f.prefix.trim(),
    start: f.start.trim(),
    end: f.end.trim(),
    preferred_lifetime: f.preferred_lifetime,
    valid_lifetime: f.valid_lifetime,
    rapid_commit: f.rapid_commit,
    preference: f.preference,
    enabled: f.enabled,
    pd_pool: pd,
    pd_length: pd ? f.pd_length : 0,
  };
}

function Scope6Fields({ f, setF, errors, disabled, fixedPrefix }: { f: Scope6Form; setF: (f: Scope6Form) => void; errors: Errors; disabled?: boolean; fixedPrefix?: boolean }) {
  const num = (k: keyof Scope6Form) => (e: { target: { value: string } }) => setF({ ...f, [k]: Number(e.target.value) });
  const str = (k: keyof Scope6Form) => (e: { target: { value: string } }) => setF({ ...f, [k]: e.target.value });
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Input label={m("name")} name="name" value={f.name} onChange={str("name")} error={errors.name} disabled={disabled} placeholder={m("namePlaceholder")} />
      <Input
        label={m("prefixLink")}
        name="prefix"
        value={f.prefix}
        onChange={str("prefix")}
        error={errors.prefix}
        disabled={disabled || fixedPrefix}
        placeholder="2001:db8:1::/64"
        hint={fixedPrefix ? m("prefixFixed") : m("prefixHint")}
      />
      <Input
        label={m("startAddress")}
        name="start"
        value={f.start}
        onChange={str("start")}
        error={errors.start}
        disabled={disabled}
        placeholder="2001:db8:1::100"
        hint={m("startHint")}
      />
      <Input label={m("endAddress")} name="end" value={f.end} onChange={str("end")} error={errors.end} disabled={disabled} placeholder="2001:db8:1::ffff" />
      <Input
        label={m("preferredLifetime")}
        name="preferred_lifetime"
        type="number"
        min={1}
        value={String(f.preferred_lifetime)}
        onChange={num("preferred_lifetime")}
        error={errors.preferred_lifetime}
        disabled={disabled}
        hint={formatDuration(f.preferred_lifetime)}
      />
      <Input
        label={m("validLifetime")}
        name="valid_lifetime"
        type="number"
        min={60}
        value={String(f.valid_lifetime)}
        onChange={num("valid_lifetime")}
        error={errors.valid_lifetime}
        disabled={disabled}
        hint={formatDuration(f.valid_lifetime)}
      />
      <Input
        label={m("pdPool")}
        name="pd_pool"
        value={f.pd_pool}
        onChange={str("pd_pool")}
        error={errors.pd_pool}
        disabled={disabled}
        placeholder="2001:db8:100::/40"
        hint={m("pdPoolHint")}
      />
      <Input
        label={m("pdLength")}
        name="pd_length"
        type="number"
        min={1}
        max={64}
        value={String(f.pd_length)}
        onChange={num("pd_length")}
        error={errors.pd_length}
        disabled={disabled || !f.pd_pool.trim()}
        hint={m("pdLengthHint")}
      />
      <Input
        label={m("preference")}
        name="preference"
        type="number"
        min={0}
        max={255}
        value={String(f.preference)}
        onChange={num("preference")}
        error={errors.preference}
        disabled={disabled}
        hint={m("preferenceHint")}
      />
      <div className="flex flex-col justify-end gap-2 pb-2">
        <Toggle label={m("rapidCommit")} checked={f.rapid_commit} onChange={(v) => setF({ ...f, rapid_commit: v })} disabled={disabled} />
        <Toggle label={m("scopeActive")} checked={f.enabled} onChange={(v) => setF({ ...f, enabled: v })} disabled={disabled} />
      </div>
      <div className="sm:col-span-2">
        <TextArea label={m("description")} rows={2} value={f.description} onChange={str("description")} disabled={disabled} />
      </div>
    </div>
  );
}

function CreateScope6({ onClose }: { onClose: () => void }) {
  const [f, setF] = useState<Scope6Form>(emptyScope6);
  const [errors, setErrors] = useState<Errors>({});
  const [error, setError] = useState("");
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { toast } = useFeedback();
  const create = useMutation({
    mutationFn: () => unwrap(api.POST("/v6/scopes", { body: scope6Body(f) })),
    onSuccess: (sc) => {
      qc.invalidateQueries({ queryKey: ["scopes6"] });
      qc.invalidateQueries({ queryKey: ["stats6"] });
      toast(m("scopeCreated", { name: sc.name }));
      navigate(`/ipv6/scopes/${sc.id}`);
    },
    onError: (e) => {
      if (e instanceof ApiError) setErrors(e.fields);
      setError(errorMessage(e));
    },
  });
  return (
    <Modal
      title={m("newScope")}
      onClose={onClose}
      wide
      footer={
        <>
          <Button onClick={onClose}>{c("cancel")}</Button>
          <Button
            variant="primary"
            disabled={create.isPending}
            onClick={() => {
              const e = validateScope6(f);
              setErrors(e);
              if (!hasErrors(e)) create.mutate();
            }}
          >
            {m("createScope")}
          </Button>
        </>
      }
    >
      {error && <Alert>{error}</Alert>}
      <Scope6Fields f={f} setF={setF} errors={errors} />
      <p className="text-xs text-slate-500">{m("createNote")}</p>
    </Modal>
  );
}

type DetailTab = "overview" | "exclusions" | "options" | "reservations" | "leases";

export function IPv6ScopeDetailPage() {
  const id = Number(useParams().id);
  const [tab, setTab] = useState<DetailTab>("overview");
  const scope = useQuery({ queryKey: ["scope6", id], queryFn: () => unwrap(api.GET("/v6/scopes/{id}", { params: { path: { id } } })) });
  if (scope.isLoading) return <Spinner />;
  if (scope.error || !scope.data) return <Alert>{errorMessage(scope.error)}</Alert>;
  const s = scope.data;
  return (
    <>
      <PageHeader
        title={s.name}
        subtitle={
          <>
            <Link to="/ipv6" className="text-teal-700 hover:underline dark:text-teal-400">
              IPv6
            </Link>{" "}
            / <span className="font-mono">{s.prefix}</span> {s.enabled ? <Badge color="green">{c("active")}</Badge> : <Badge color="amber">{c("disabled")}</Badge>}
          </>
        }
      />
      <Tabs<DetailTab>
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "overview", label: m("overview") },
          { id: "exclusions", label: m("exclusions") },
          { id: "options", label: m("options") },
          { id: "reservations", label: m("reservations") },
          { id: "leases", label: m("leases") },
        ]}
      />
      {tab === "overview" && <Overview6 scope={s} />}
      {tab === "exclusions" && <Exclusions6 scope={s} />}
      {tab === "options" && (
        <Card title={m("scopeOptions")} actions={<span className="text-xs text-slate-500">{m("scopeOptionsHint")}</span>}>
          <Options6Editor owner="scope" ownerId={s.id} />
        </Card>
      )}
      {tab === "reservations" && <Reservations6Table scopeId={s.id} />}
      {tab === "leases" && <Leases6Table scopeId={s.id} />}
    </>
  );
}

function Overview6({ scope }: { scope: S["Scope6"] }) {
  const { canWrite } = useAuth();
  const stats = useStats6();
  const qc = useQueryClient();
  const { toast } = useFeedback();
  const [f, setF] = useState<Scope6Form>(scope);
  const [errors, setErrors] = useState<Errors>({});
  useEffect(() => setF(scope), [scope]);
  const save = useMutation({
    mutationFn: () => unwrap(api.PUT("/v6/scopes/{id}", { params: { path: { id: scope.id } }, body: scope6Body(f) })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["scope6", scope.id] });
      qc.invalidateQueries({ queryKey: ["scopes6"] });
      qc.invalidateQueries({ queryKey: ["stats6"] });
      toast(m("scopeSaved"));
    },
    onError: (e) => {
      if (e instanceof ApiError) setErrors(e.fields);
      toast(errorMessage(e), "error");
    },
  });
  const st = stats.data?.find((x) => x.scope_id === scope.id);
  return (
    <div className="grid gap-6 lg:grid-cols-[2fr_3fr]">
      <Card title={m("statistics")}>
        {st ? (
          <dl className="grid grid-cols-2 gap-y-1 text-sm">
            <dt className="text-slate-500">{m("addressesInRange")}</dt>
            <dd className="tabular-nums">{scope.start ? st.size : "–"}</dd>
            <dt className="text-slate-500">{m("assignedAddresses")}</dt>
            <dd className="font-semibold tabular-nums">{st.addresses_in_use}</dd>
            <dt className="text-slate-500">{m("declined")}</dt>
            <dd className="tabular-nums">{st.declined}</dd>
            <dt className="text-slate-500">{m("prefixCapacity")}</dt>
            <dd className="tabular-nums">{scope.pd_pool ? st.prefix_capacity : "–"}</dd>
            <dt className="text-slate-500">{m("delegatedPrefixes")}</dt>
            <dd className="font-semibold tabular-nums">{scope.pd_pool ? st.prefixes_in_use : "–"}</dd>
          </dl>
        ) : (
          <Spinner />
        )}
      </Card>
      <Card title={m("settings")}>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const v = validateScope6(f);
            setErrors(v);
            if (!hasErrors(v)) save.mutate();
          }}
          className="space-y-4"
        >
          <Scope6Fields f={f} setF={setF} errors={errors} disabled={!canWrite} fixedPrefix />
          {canWrite && (
            <Button type="submit" variant="primary" disabled={save.isPending}>
              {c("save")}
            </Button>
          )}
        </form>
      </Card>
    </div>
  );
}

function Exclusions6({ scope }: { scope: S["Scope6"] }) {
  const { canWrite } = useAuth();
  const qc = useQueryClient();
  const { toast } = useFeedback();
  const key = ["exclusions6", scope.id];
  const ex = useQuery({ queryKey: key, queryFn: () => unwrap(api.GET("/v6/scopes/{id}/exclusions", { params: { path: { id: scope.id } } })) });
  const [start, setStart] = useState("");
  const [end, setEnd] = useState("");
  const [errors, setErrors] = useState<Errors>({});
  const add = useMutation({
    mutationFn: () => unwrap(api.POST("/v6/scopes/{id}/exclusions", { params: { path: { id: scope.id } }, body: { start: start.trim(), end: (end || start).trim() } })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: key });
      setStart("");
      setEnd("");
      setErrors({});
      toast(m("exclusionAdded"));
    },
    onError: (e) => {
      if (e instanceof ApiError) setErrors(e.fields);
      toast(errorMessage(e), "error");
    },
  });
  const del = useMutation({
    mutationFn: (eid: number) => unwrap(api.DELETE("/v6/scopes/{id}/exclusions/{exclusionId}", { params: { path: { id: scope.id, exclusionId: eid } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
    onError: (e) => toast(errorMessage(e), "error"),
  });
  return (
    <Card title={m("exclusionRanges")} actions={<span className="text-xs text-slate-500">{m("exclusionRangesHint")}</span>}>
      <Table head={[m("from"), m("to"), ""]} empty={ex.data?.length === 0}>
        {ex.data?.map((e) => (
          <tr key={e.id}>
            <Td mono>{e.start}</Td>
            <Td mono>{e.end}</Td>
            <Td className="text-right">
              {canWrite && (
                <Button size="sm" variant="ghost" onClick={() => del.mutate(e.id)}>
                  {c("remove")}
                </Button>
              )}
            </Td>
          </tr>
        ))}
      </Table>
      {canWrite && (
        <form
          className="mt-4 grid gap-3 sm:grid-cols-[1fr_1fr_auto] sm:items-end"
          onSubmit={(e) => {
            e.preventDefault();
            if (!start.trim()) return setErrors({ start: m("addressRequired") });
            add.mutate();
          }}
        >
          <Input label={m("from")} value={start} onChange={(e) => setStart(e.target.value)} error={errors.start} placeholder={scope.start || "2001:db8:1::10"} />
          <Input label={m("to")} value={end} onChange={(e) => setEnd(e.target.value)} error={errors.end} hint={m("toHint")} />
          <Button type="submit" variant="primary">
            {c("add")}
          </Button>
        </form>
      )}
    </Card>
  );
}

const emptyRes6 = { scope_id: 0, duid: "", iaid: "", ip: "", hostname: "", description: "" };

function reservation6Body(r: typeof emptyRes6): S["Reservation6Input"] {
  return {
    scope_id: r.scope_id,
    duid: r.duid.trim(),
    iaid: r.iaid.trim() === "" ? -1 : Number(r.iaid),
    ip: r.ip.trim(),
    hostname: r.hostname.trim(),
    description: r.description,
  };
}

function validateRes6(r: typeof emptyRes6): Errors {
  const e: Errors = {};
  if (!/^[0-9a-fA-F]{2}([:-]?[0-9a-fA-F]{2}){2,129}$/.test(r.duid.trim())) e.duid = m("duidInvalid");
  if (r.iaid.trim() !== "" && !/^\d+$/.test(r.iaid.trim())) e.iaid = m("iaidInvalid");
  if (!r.ip.trim().includes(":")) e.ip = m("ipRequired");
  if (!r.scope_id) e.scope_id = m("scopeRequired");
  return e;
}

/** DHCPv6 reservations; scopeId 0 = all scopes. */
export function Reservations6Table({ scopeId }: { scopeId: number }) {
  const { canWrite } = useAuth();
  const scopes = useScopes6();
  const qc = useQueryClient();
  const { toast, confirm } = useFeedback();
  const list = useQuery({
    queryKey: ["reservations6", scopeId],
    queryFn: () => unwrap(api.GET("/v6/reservations", { params: { query: scopeId ? { scope_id: scopeId } : {} } })),
  });
  const [row, setRow] = useState({ ...emptyRes6, scope_id: scopeId });
  const [errors, setErrors] = useState<Errors>({});
  const [edit, setEdit] = useState<S["Reservation6"] | null>(null);
  const scopeById = new Map(scopes.data?.map((s) => [s.id, s]));
  const add = useMutation({
    mutationFn: () => unwrap(api.POST("/v6/reservations", { body: reservation6Body(row) })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["reservations6"] });
      setRow({ ...emptyRes6, scope_id: row.scope_id });
      setErrors({});
      toast(m("reservationCreated"));
    },
    onError: (e) => {
      if (e instanceof ApiError) setErrors(e.fields);
      toast(errorMessage(e), "error");
    },
  });
  const del = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/v6/reservations/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["reservations6"] });
      toast(m("reservationDeleted"));
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  if (list.isLoading) return <Spinner />;
  return (
    <div className="space-y-4">
      {canWrite && (
        <Card title={m("newReservation")}>
          <form
            className="grid gap-3 sm:grid-cols-3 lg:grid-cols-[2fr_1fr_2fr_1fr_1fr_auto] lg:items-end"
            onSubmit={(e) => {
              e.preventDefault();
              const v = validateRes6(row);
              setErrors(v);
              if (!hasErrors(v)) add.mutate();
            }}
          >
            <Input label="DUID" value={row.duid} onChange={(e) => setRow({ ...row, duid: e.target.value })} error={errors.duid} placeholder="00:03:00:01:…" />
            <Input label="IAID" value={row.iaid} onChange={(e) => setRow({ ...row, iaid: e.target.value })} error={errors.iaid} placeholder={m("any")} />
            <Input label={m("ipv6Address")} value={row.ip} onChange={(e) => setRow({ ...row, ip: e.target.value })} error={errors.ip} />
            <Input label={m("hostname")} value={row.hostname} onChange={(e) => setRow({ ...row, hostname: e.target.value })} />
            {scopeId ? (
              <Input label={m("description")} value={row.description} onChange={(e) => setRow({ ...row, description: e.target.value })} />
            ) : (
              <Select label={m("scope")} value={String(row.scope_id)} onChange={(e) => setRow({ ...row, scope_id: Number(e.target.value) })} error={errors.scope_id}>
                <option value="0">{m("scopePlaceholder")}</option>
                {scopes.data?.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name}
                  </option>
                ))}
              </Select>
            )}
            <Button type="submit" variant="primary" disabled={add.isPending}>
              {c("create")}
            </Button>
          </form>
        </Card>
      )}
      <div className="rounded-lg border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <Table head={[m("ipv6Address"), "DUID", "IAID", m("hostname"), m("scope"), m("description"), ""]} empty={list.data?.length === 0}>
          {list.data?.map((r) => (
            <tr key={r.id}>
              <Td mono>{r.ip}</Td>
              <Td mono className="max-w-xs truncate">
                {r.duid}
              </Td>
              <Td mono>{r.iaid === -1 ? m("any") : r.iaid}</Td>
              <Td>{r.hostname || "–"}</Td>
              <Td>{scopeById.get(r.scope_id)?.name}</Td>
              <Td className="max-w-xs truncate">{r.description}</Td>
              <Td className="text-right">
                <div className="flex justify-end gap-1">
                  <Button size="sm" variant="ghost" onClick={() => setEdit(r)}>
                    {canWrite ? c("edit") : c("details")}
                  </Button>
                  {canWrite && (
                    <Button size="sm" variant="ghost" onClick={async () => (await confirm(m("deleteReservation", { ip: r.ip }), { danger: true })) && del.mutate(r.id)}>
                      {c("delete")}
                    </Button>
                  )}
                </div>
              </Td>
            </tr>
          ))}
        </Table>
      </div>
      {edit && <EditReservation6 res={edit} onClose={() => setEdit(null)} />}
    </div>
  );
}

function EditReservation6({ res, onClose }: { res: S["Reservation6"]; onClose: () => void }) {
  const { canWrite } = useAuth();
  const [f, setF] = useState({ ...res, iaid: res.iaid === -1 ? "" : String(res.iaid) });
  const [errors, setErrors] = useState<Errors>({});
  const qc = useQueryClient();
  const { toast } = useFeedback();
  const save = useMutation({
    mutationFn: () => unwrap(api.PUT("/v6/reservations/{id}", { params: { path: { id: res.id } }, body: reservation6Body(f) })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["reservations6"] });
      toast(m("reservationSaved"));
      onClose();
    },
    onError: (e) => {
      if (e instanceof ApiError) setErrors(e.fields);
      toast(errorMessage(e), "error");
    },
  });
  return (
    <Modal
      title={m("reservationTitle", { ip: res.ip })}
      onClose={onClose}
      wide
      footer={
        canWrite && (
          <Button
            variant="primary"
            disabled={save.isPending}
            onClick={() => {
              const e = validateRes6(f);
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
        <Input label={m("ipv6Address")} value={f.ip} onChange={(e) => setF({ ...f, ip: e.target.value })} error={errors.ip} disabled={!canWrite} />
        <Input label={m("hostname")} value={f.hostname} onChange={(e) => setF({ ...f, hostname: e.target.value })} disabled={!canWrite} />
        <Input label="DUID" value={f.duid} onChange={(e) => setF({ ...f, duid: e.target.value })} error={errors.duid} disabled={!canWrite} />
        <Input label="IAID" value={f.iaid} onChange={(e) => setF({ ...f, iaid: e.target.value })} error={errors.iaid} disabled={!canWrite} hint={m("iaidHint")} />
        <div className="sm:col-span-2">
          <Input label={m("description")} value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} disabled={!canWrite} />
        </div>
      </div>
      <Card title={m("reservationOptions")}>
        <Options6Editor owner="reservation" ownerId={res.id} />
      </Card>
    </Modal>
  );
}

const typeHints6 = (): Record<string, string> => ({
  "ipv6-list": m("hintIpv6List"),
  "domain-list": m("hintDomainList"),
  string: m("hintString"),
  uint8: "0–255",
  uint16: "0–65535",
  uint32: "0–4294967295",
  hex: m("hintHex"),
  "ntp-servers": m("hintNtpServers"),
});
const optionTypes6: S["Option6Type"][] = ["ipv6-list", "domain-list", "string", "uint8", "uint16", "uint32", "hex", "ntp-servers"];

/** Lists and edits the DHCPv6 option values of one owner. */
export function Options6Editor({ owner, ownerId }: { owner: S["Option6Owner"]; ownerId: number }) {
  const { canWrite } = useAuth();
  const qc = useQueryClient();
  const { toast, confirm } = useFeedback();
  const key = ["options6", owner, ownerId];
  const opts = useQuery({ queryKey: key, queryFn: () => unwrap(api.GET("/v6/options/{owner}/{ownerId}", { params: { path: { owner, ownerId } } })) });
  const defs = useQuery({ queryKey: ["option-definitions6"], queryFn: () => unwrap(api.GET("/v6/option-definitions")), staleTime: 60_000 });
  const [code, setCode] = useState("");
  const [custom, setCustom] = useState("");
  const [type, setType] = useState<S["Option6Type"]>("hex");
  const [value, setValue] = useState("");
  const [error, setError] = useState("");
  const save = useMutation({
    mutationFn: (v: { code: number; value: string; type?: S["Option6Type"] }) =>
      unwrap(api.PUT("/v6/options/{owner}/{ownerId}/{code}", { params: { path: { owner, ownerId, code: v.code } }, body: { value: v.value, ...(v.type ? { type: v.type } : {}) } })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: key });
      setCode("");
      setCustom("");
      setValue("");
      setError("");
      toast(m("optionSaved"));
    },
    onError: (e) => setError(errorMessage(e)),
  });
  const del = useMutation({
    mutationFn: (c: number) => unwrap(api.DELETE("/v6/options/{owner}/{ownerId}/{code}", { params: { path: { owner, ownerId, code: c } } })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: key });
      toast(m("optionRemoved"));
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  if (opts.isLoading || defs.isLoading) return <Spinner />;
  const selected = defs.data?.find((d) => String(d.code) === code);
  const hintType = code === "other" ? type : selected?.type;
  return (
    <div className="space-y-4">
      <Table head={[m("code"), m("name"), m("type"), m("value"), ""]} empty={opts.data?.length === 0}>
        {opts.data?.map((o) => (
          <tr key={o.code}>
            <Td mono>{o.code}</Td>
            <Td>{o.name}</Td>
            <Td>
              <Badge>{o.type}</Badge>
            </Td>
            <Td mono className="max-w-md truncate whitespace-normal">
              {o.value}
            </Td>
            <Td className="text-right">
              {canWrite && (
                <div className="flex justify-end gap-1">
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      const known = defs.data?.some((d) => d.code === o.code);
                      setCode(known ? String(o.code) : "other");
                      setCustom(known ? "" : String(o.code));
                      setType(o.type);
                      setValue(o.value);
                    }}
                  >
                    {c("edit")}
                  </Button>
                  <Button size="sm" variant="ghost" onClick={async () => (await confirm(m("removeOption", { code: o.code, name: o.name }), { danger: true })) && del.mutate(o.code)}>
                    {c("remove")}
                  </Button>
                </div>
              )}
            </Td>
          </tr>
        ))}
      </Table>
      {canWrite && (
        <form
          className="grid gap-3 rounded-md border border-dashed border-slate-300 p-3 sm:grid-cols-[minmax(0,2fr)_minmax(0,3fr)_auto] sm:items-end dark:border-slate-700"
          onSubmit={(e) => {
            e.preventDefault();
            if (!code) return setError(m("selectOption"));
            if (code === "other") {
              const n = Number(custom);
              if (!Number.isInteger(n) || n < 1 || n > 65535) return setError(m("codeRange"));
              return save.mutate({ code: n, value, type });
            }
            save.mutate({ code: Number(code), value });
          }}
        >
          <Select label={m("option")} value={code} onChange={(e) => setCode(e.target.value)}>
            <option value="">{c("select")}</option>
            {defs.data?.map((d) => (
              <option key={d.code} value={d.code}>
                {d.code} – {d.name}
              </option>
            ))}
            <option value="other">{m("otherOption")}</option>
          </Select>
          <Input label={m("value")} value={value} onChange={(e) => setValue(e.target.value)} hint={hintType ? `${hintType}: ${typeHints6()[hintType]}` : undefined} />
          <Button type="submit" variant="primary" disabled={save.isPending}>
            {c("set")}
          </Button>
          {code === "other" && (
            <div className="grid gap-3 sm:col-span-3 sm:grid-cols-2">
              <Input label={m("code")} type="number" min={1} max={65535} value={custom} onChange={(e) => setCustom(e.target.value)} />
              <Select label={m("type")} value={type} onChange={(e) => setType(e.target.value as S["Option6Type"])}>
                {optionTypes6.map((t) => (
                  <option key={t} value={t}>
                    {t}
                  </option>
                ))}
              </Select>
            </div>
          )}
          {error && (
            <div className="sm:col-span-3">
              <Alert>{error}</Alert>
            </div>
          )}
        </form>
      )}
    </div>
  );
}

const stateColor: Record<string, "green" | "slate" | "amber" | "red"> = { active: "green", expired: "slate", released: "amber", declined: "red" };

/** DHCPv6 bindings; scopeId 0 = all scopes. */
export function Leases6Table({ scopeId }: { scopeId: number }) {
  const { canOperate } = useAuth();
  const scopes = useScopes6();
  const qc = useQueryClient();
  const { toast, confirm } = useFeedback();
  const [q, setQ] = useState("");
  const [query, setQuery] = useState("");
  useEffect(() => {
    const t = setTimeout(() => setQuery(q), 300);
    return () => clearTimeout(t);
  }, [q]);
  const leases = useQuery({
    queryKey: ["leases6", scopeId, query],
    queryFn: () => unwrap(api.GET("/v6/leases", { params: { query: { ...(scopeId ? { scope_id: scopeId } : {}), ...(query ? { q: query } : {}) } } })),
    refetchInterval: 10000,
  });
  const del = useMutation({
    mutationFn: (address: string) => unwrap(api.DELETE("/v6/leases/{address}", { params: { path: { address } } })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["leases6"] });
      qc.invalidateQueries({ queryKey: ["stats6"] });
      toast(m("leaseDeleted"));
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const scopeName = new Map(scopes.data?.map((s) => [s.id, s.name]));
  return (
    <div className="space-y-3">
      <div className="w-full max-w-sm">
        <Input label={c("search")} placeholder={m("searchHint")} value={q} onChange={(e) => setQ(e.target.value)} />
      </div>
      <div className="rounded-lg border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        {leases.isLoading ? (
          <Spinner />
        ) : (
          <Table head={[m("addressPrefix"), m("kind"), "DUID", "IAID", m("hostname"), m("scope"), m("state"), m("expires"), ""]} empty={leases.data?.length === 0}>
            {leases.data?.map((l) => {
              const label = l.kind === "pd" ? `${l.address}/${l.prefix_len}` : l.address;
              return (
                <tr key={l.address} data-testid={`lease6-${l.address}`}>
                  <Td mono>{label}</Td>
                  <Td>{l.kind === "pd" ? <Badge color="blue">{m("kindPrefix")}</Badge> : <Badge>{m("kindAddress")}</Badge>}</Td>
                  <Td mono className="max-w-48 truncate">
                    <span title={l.duid}>{l.duid}</span>
                  </Td>
                  <Td mono>{l.iaid}</Td>
                  <Td>{l.hostname || "–"}</Td>
                  <Td>{scopeName.get(l.scope_id) ?? l.scope_id}</Td>
                  <Td>
                    <Badge color={stateColor[l.state]}>{stateLabel(l.state)}</Badge>
                  </Td>
                  <Td>
                    <span title={m("lifetimes", { preferred: formatDateTime(l.preferred_until), valid: formatDateTime(l.expires_at) })}>{relativeTime(l.expires_at)}</span>
                  </Td>
                  <Td className="text-right">
                    {canOperate && (
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={async () => (await confirm(m(l.kind === "pd" ? "deleteLeasePrefix" : "deleteLeaseAddress", { label }), { danger: true })) && del.mutate(l.address)}
                      >
                        {c("delete")}
                      </Button>
                    )}
                  </Td>
                </tr>
              );
            })}
          </Table>
        )}
      </div>
    </div>
  );
}

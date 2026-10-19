import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router";
import { api, ApiError, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { Alert, Button, Card, Input, Select, Toggle, useFeedback } from "../components/ui";
import { messages } from "../i18n";
import { c } from "../i18n/common";
import { useDnsZones } from "./DNS";

const m = messages({
  en: {
    never: "Never",
    neverHint: "No DNS updates for this scope.",
    client: "On client request",
    clientHint:
      "As requested by the client in option 81: A and PTR, or only PTR if the client registers its A record itself. Clients without option 81 are not registered.",
    always: "Always",
    alwaysHint: "A and PTR for every client that sends a name (option 81 or host name option 12).",
    saved: "DNS settings saved",
    title: "Dynamic DNS updates",
    noZone: "No DNS zone has been set up yet – create a zone and a TSIG key under",
    noZoneLink: "DNS updates",
    noZoneEnd: ".",
    register: "Register records",
    domain: "Domain",
    domainPlaceholder: "from option 15 (domain name)",
    domainHint: "Name = host name of the client + this domain",
    ttl: "TTL (seconds)",
    ttlHint: "0 = automatic (one third of the lease duration)",
    cleanup: "Remove records when the lease ends",
    protect: "Name protection (DHCID): never overwrite names of other clients or names created manually",
  },
  de: {
    never: "Nie",
    neverHint: "Keine DNS-Updates für diesen Scope.",
    client: "Auf Anforderung des Clients",
    clientHint:
      "Wie vom Client in Option 81 gewünscht: A und PTR, oder nur PTR, wenn der Client seinen A-Record selbst einträgt. Clients ohne Option 81 werden nicht registriert.",
    always: "Immer",
    alwaysHint: "A und PTR für jeden Client, der einen Namen sendet (Option 81 oder Hostname-Option 12).",
    saved: "DNS-Einstellungen gespeichert",
    title: "Dynamische DNS-Updates",
    noZone: "Es ist noch keine DNS-Zone eingerichtet – unter",
    noZoneLink: "DNS-Updates",
    noZoneEnd: " Zone und TSIG-Schlüssel anlegen.",
    register: "Records registrieren",
    domain: "Domäne",
    domainPlaceholder: "aus Option 15 (Domänenname)",
    domainHint: "Name = Hostname des Clients + diese Domäne",
    ttl: "TTL (Sekunden)",
    ttlHint: "0 = automatisch (ein Drittel der Lease-Dauer)",
    cleanup: "Records entfernen, wenn der Lease endet",
    protect: "Namensschutz (DHCID): fremde oder manuell angelegte Namen nie überschreiben",
  },
});

type Mode = S["DnsSettings"]["mode"];

const modes = (): { v: Mode; l: string; hint: string }[] => [
  { v: "never", l: m("never"), hint: m("neverHint") },
  { v: "client", l: m("client"), hint: m("clientHint") },
  { v: "always", l: m("always"), hint: m("alwaysHint") },
];

function formOf(scope: S["Scope"]) {
  return {
    mode: scope.dns.mode,
    domain: scope.dns.domain ?? "",
    ttl: scope.dns.ttl ?? 0,
    cleanup: scope.dns.cleanup ?? true,
    protect: scope.dns.protect ?? true,
  };
}

/** DNS update settings of a scope (Windows: DNS tab of the scope). */
export function ScopeDNS({ scope }: { scope: S["Scope"] }) {
  const { canWrite } = useAuth();
  const zones = useDnsZones();
  const qc = useQueryClient();
  const { toast } = useFeedback();
  const [f, setF] = useState(() => formOf(scope));
  const [errors, setErrors] = useState<Record<string, string>>({});
  useEffect(() => setF(formOf(scope)), [scope]);

  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT("/scopes/{id}", {
          params: { path: { id: scope.id } },
          body: {
            name: scope.name,
            description: scope.description,
            subnet: scope.subnet,
            start: scope.start,
            end: scope.end,
            lease_time: scope.lease_time,
            enabled: scope.enabled,
            dns: f,
          },
        }),
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["scope", scope.id] });
      qc.invalidateQueries({ queryKey: ["scopes"] });
      setErrors({});
      toast(m("saved"));
    },
    onError: (e) => {
      if (e instanceof ApiError) setErrors(e.fields);
      toast(errorMessage(e), "error");
    },
  });
  const list = modes();
  const mode = list.find((x) => x.v === f.mode) ?? list[0];
  const off = f.mode === "never";
  return (
    <Card title={m("title")}>
      {!off && zones.data?.length === 0 && (
        <div className="mb-4">
          <Alert kind="info">
            {m("noZone")}{" "}
            <Link to="/dns" className="font-medium underline">
              {m("noZoneLink")}
            </Link>
            {m("noZoneEnd")}
          </Alert>
        </div>
      )}
      <form
        className="grid gap-4 sm:grid-cols-2"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate();
        }}
      >
        <div className="sm:col-span-2">
          <Select label={m("register")} value={f.mode} onChange={(e) => setF({ ...f, mode: e.target.value as Mode })} hint={mode.hint} disabled={!canWrite} error={errors["dns.mode"]}>
            {list.map((x) => (
              <option key={x.v} value={x.v}>
                {x.l}
              </option>
            ))}
          </Select>
        </div>
        <Input
          label={m("domain")}
          value={f.domain}
          onChange={(e) => setF({ ...f, domain: e.target.value })}
          placeholder={m("domainPlaceholder")}
          hint={m("domainHint")}
          disabled={!canWrite || off}
          error={errors["dns.domain"]}
        />
        <Input
          label={m("ttl")}
          type="number"
          min={0}
          value={f.ttl}
          onChange={(e) => setF({ ...f, ttl: Number(e.target.value) })}
          hint={m("ttlHint")}
          disabled={!canWrite || off}
          error={errors["dns.ttl"]}
        />
        <div className="sm:col-span-2 space-y-3">
          <Toggle label={m("cleanup")} checked={f.cleanup} onChange={(v) => setF({ ...f, cleanup: v })} disabled={!canWrite || off} />
          <Toggle
            label={m("protect")}
            checked={f.protect}
            onChange={(v) => setF({ ...f, protect: v })}
            disabled={!canWrite || off}
          />
        </div>
        {canWrite && (
          <div className="sm:col-span-2">
            <Button type="submit" variant="primary" disabled={save.isPending}>
              {c("save")}
            </Button>
          </div>
        )}
      </form>
    </Card>
  );
}

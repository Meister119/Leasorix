import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router";
import { api, ApiError, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { Alert, Button, Card, Input, Select, Toggle, useFeedback } from "../components/ui";
import { useDnsZones } from "./DNS";

type Mode = S["DnsSettings"]["mode"];

const modes: { v: Mode; l: string; hint: string }[] = [
  { v: "never", l: "Nie", hint: "Keine DNS-Updates für diesen Scope." },
  {
    v: "client",
    l: "Auf Anforderung des Clients",
    hint: "Wie vom Client in Option 81 gewünscht: A und PTR, oder nur PTR, wenn der Client seinen A-Record selbst einträgt. Clients ohne Option 81 werden nicht registriert.",
  },
  { v: "always", l: "Immer", hint: "A und PTR für jeden Client, der einen Namen sendet (Option 81 oder Hostname-Option 12)." },
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
      toast("DNS-Einstellungen gespeichert");
    },
    onError: (e) => {
      if (e instanceof ApiError) setErrors(e.fields);
      toast(errorMessage(e), "error");
    },
  });
  const mode = modes.find((m) => m.v === f.mode) ?? modes[0];
  const off = f.mode === "never";
  return (
    <Card title="Dynamische DNS-Updates">
      {!off && zones.data?.length === 0 && (
        <div className="mb-4">
          <Alert kind="info">
            Es ist noch keine DNS-Zone eingerichtet – unter{" "}
            <Link to="/dns" className="font-medium underline">
              DNS-Updates
            </Link>{" "}
            Zone und TSIG-Schlüssel anlegen.
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
          <Select label="Records registrieren" value={f.mode} onChange={(e) => setF({ ...f, mode: e.target.value as Mode })} hint={mode.hint} disabled={!canWrite} error={errors["dns.mode"]}>
            {modes.map((m) => (
              <option key={m.v} value={m.v}>
                {m.l}
              </option>
            ))}
          </Select>
        </div>
        <Input
          label="Domäne"
          value={f.domain}
          onChange={(e) => setF({ ...f, domain: e.target.value })}
          placeholder="aus Option 15 (Domänenname)"
          hint="Name = Hostname des Clients + diese Domäne"
          disabled={!canWrite || off}
          error={errors["dns.domain"]}
        />
        <Input
          label="TTL (Sekunden)"
          type="number"
          min={0}
          value={f.ttl}
          onChange={(e) => setF({ ...f, ttl: Number(e.target.value) })}
          hint="0 = automatisch (ein Drittel der Lease-Dauer)"
          disabled={!canWrite || off}
          error={errors["dns.ttl"]}
        />
        <div className="sm:col-span-2 space-y-3">
          <Toggle label="Records entfernen, wenn der Lease endet" checked={f.cleanup} onChange={(v) => setF({ ...f, cleanup: v })} disabled={!canWrite || off} />
          <Toggle
            label="Namensschutz (DHCID): fremde oder manuell angelegte Namen nie überschreiben"
            checked={f.protect}
            onChange={(v) => setF({ ...f, protect: v })}
            disabled={!canWrite || off}
          />
        </div>
        {canWrite && (
          <div className="sm:col-span-2">
            <Button type="submit" variant="primary" disabled={save.isPending}>
              Speichern
            </Button>
          </div>
        )}
      </form>
    </Card>
  );
}

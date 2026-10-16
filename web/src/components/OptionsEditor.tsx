import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { Alert, Badge, Button, Input, Select, Spinner, Table, Td, useFeedback } from "./ui";

const typeHints: Record<string, string> = {
  ip: "z. B. 192.168.1.1",
  "ip-list": "mehrere Adressen mit Komma trennen",
  string: "Text",
  uint8: "0–255",
  uint16: "0–65535",
  uint32: "0–4294967295",
  int32: "Ganzzahl mit Vorzeichen",
  bool: "true oder false",
  hex: "z. B. 01:0a:ff",
  "domain-list": "z. B. example.com, corp.example.com",
  routes: "Ziel/Präfix Gateway, z. B. 10.0.0.0/8 192.168.1.1",
};

export function useOptionDefinitions() {
  return useQuery({ queryKey: ["option-definitions"], queryFn: () => unwrap(api.GET("/option-definitions")), staleTime: 60_000 });
}

/** Lists and edits the option values of one owner (server, scope, policy or reservation). */
export function OptionsEditor({ owner, ownerId }: { owner: S["OptionOwner"]; ownerId: number }) {
  const { canWrite } = useAuth();
  const qc = useQueryClient();
  const { toast, confirm } = useFeedback();
  const key = ["options", owner, ownerId];
  const opts = useQuery({
    queryKey: key,
    queryFn: () => unwrap(api.GET("/options/{owner}/{ownerId}", { params: { path: { owner, ownerId } } })),
  });
  const defs = useOptionDefinitions();
  const [code, setCode] = useState("");
  const [value, setValue] = useState("");
  const [error, setError] = useState("");

  const save = useMutation({
    mutationFn: (v: { code: number; value: string }) =>
      unwrap(api.PUT("/options/{owner}/{ownerId}/{code}", { params: { path: { owner, ownerId, code: v.code } }, body: { value: v.value } })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: key });
      setCode("");
      setValue("");
      setError("");
      toast("Option gespeichert");
    },
    onError: (e) => setError(errorMessage(e)),
  });
  const del = useMutation({
    mutationFn: (c: number) => unwrap(api.DELETE("/options/{owner}/{ownerId}/{code}", { params: { path: { owner, ownerId, code: c } } })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: key });
      toast("Option entfernt");
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });

  if (opts.isLoading || defs.isLoading) return <Spinner />;
  const selected = defs.data?.find((d) => String(d.code) === code);

  return (
    <div className="space-y-4">
      <Table head={["Code", "Name", "Typ", "Wert", ""]} empty={opts.data?.length === 0}>
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
                      setCode(String(o.code));
                      setValue(o.value);
                    }}
                  >
                    Bearbeiten
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={async () => (await confirm(`Option ${o.code} (${o.name}) entfernen?`, { danger: true })) && del.mutate(o.code)}
                  >
                    Entfernen
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
            if (!code) return setError("Option auswählen");
            save.mutate({ code: Number(code), value });
          }}
        >
          <Select label="Option" value={code} onChange={(e) => setCode(e.target.value)}>
            <option value="">– auswählen –</option>
            {defs.data?.map((d) => (
              <option key={d.code} value={d.code}>
                {d.code} – {d.name}
              </option>
            ))}
          </Select>
          <Input label="Wert" value={value} onChange={(e) => setValue(e.target.value)} hint={selected ? `${selected.type}: ${typeHints[selected.type]}` : undefined} />
          <Button type="submit" variant="primary" disabled={save.isPending}>
            Setzen
          </Button>
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

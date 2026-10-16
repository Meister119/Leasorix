import { useEffect, useState } from "react";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { Badge, Button, Input, PageHeader, Select, Spinner, Table, Td, useFeedback } from "../components/ui";
import { downloadFile, fileStamp } from "../lib/download";
import { formatDateTime, relativeTime, stateLabel } from "../lib/format";
import { useScopes } from "./Scopes";

export function LeasesPage() {
  const scopes = useScopes();
  const [scopeId, setScopeId] = useState(0);
  return (
    <>
      <PageHeader title="Leases" subtitle="Vergebene und zuletzt genutzte Adressen" actions={<LeaseExportButton />} />
      <div className="mb-4 max-w-xs">
        <Select label="Scope" value={String(scopeId)} onChange={(e) => setScopeId(Number(e.target.value))}>
          <option value="0">Alle Scopes</option>
          {scopes.data?.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name} ({s.subnet})
            </option>
          ))}
        </Select>
      </div>
      <LeasesTable scopeId={scopeId} />
    </>
  );
}

export function LeaseExportButton() {
  const { toast } = useFeedback();
  const [busy, setBusy] = useState(false);
  return (
    <Button
      disabled={busy}
      onClick={async () => {
        setBusy(true);
        try {
          const data = await unwrap(api.GET("/leases/export"));
          downloadFile(`leasorix-leases-${fileStamp()}.json`, JSON.stringify(data, null, 2));
          toast(`${data.leases.length} Leases exportiert`);
        } catch (e) {
          toast(errorMessage(e), "error");
        } finally {
          setBusy(false);
        }
      }}
    >
      Exportieren
    </Button>
  );
}

const stateColor: Record<string, "green" | "slate" | "amber" | "red"> = { active: "green", expired: "slate", released: "amber", declined: "red" };
const pageSize = 50;

export function LeasesTable({ scopeId }: { scopeId: number }) {
  const { canWrite, canOperate } = useAuth();
  const qc = useQueryClient();
  const { toast, confirm } = useFeedback();
  const [q, setQ] = useState("");
  const [query, setQuery] = useState("");
  const [state, setState] = useState<"" | S["LeaseState"]>("");
  const [page, setPage] = useState(0);
  useEffect(() => {
    const t = setTimeout(() => setQuery(q), 300);
    return () => clearTimeout(t);
  }, [q]);
  useEffect(() => setPage(0), [query, state, scopeId]);

  const leases = useQuery({
    queryKey: ["leases", scopeId, query, state, page],
    queryFn: () =>
      unwrap(
        api.GET("/leases", {
          params: {
            query: { ...(scopeId ? { scope_id: scopeId } : {}), ...(query ? { q: query } : {}), ...(state ? { state } : {}), offset: page * pageSize, limit: pageSize },
          },
        }),
      ),
    refetchInterval: 10000,
    placeholderData: keepPreviousData,
  });
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["leases"] });
    qc.invalidateQueries({ queryKey: ["reservations"] });
    qc.invalidateQueries({ queryKey: ["stats"] });
  };
  const convert = useMutation({
    mutationFn: (ip: string) => unwrap(api.POST("/leases/{ip}/reservation", { params: { path: { ip } }, body: {} })),
    onSuccess: (r) => {
      refresh();
      toast(`Reservierung für ${r.ip} angelegt`);
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const del = useMutation({
    mutationFn: (ip: string) => unwrap(api.DELETE("/leases/{ip}", { params: { path: { ip } } })),
    onSuccess: () => {
      refresh();
      toast("Lease gelöscht");
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });

  const total = leases.data?.total ?? 0;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-end gap-3">
        <div className="w-full max-w-sm">
          <Input label="Suche" placeholder="IP, MAC, Hostname, Client-ID" value={q} onChange={(e) => setQ(e.target.value)} />
        </div>
        <div className="w-44">
          <Select label="Zustand" value={state} onChange={(e) => setState(e.target.value as S["LeaseState"] | "")}>
            <option value="">alle</option>
            {Object.entries(stateLabel).map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </Select>
        </div>
      </div>
      <div className="rounded-lg border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        {leases.isLoading ? (
          <Spinner />
        ) : (
          <Table head={["IP", "MAC", "Hostname", "Scope", "Zustand", "Läuft ab", ""]} empty={leases.data?.items.length === 0}>
            {leases.data?.items.map((l) => (
              <tr key={l.ip} data-testid={`lease-${l.ip}`}>
                <Td mono>
                  {l.ip} {l.reserved && <Badge color="teal">reserviert</Badge>}
                </Td>
                <Td mono>{l.mac}</Td>
                <Td>{l.hostname || "–"}</Td>
                <Td>{l.scope_name}</Td>
                <Td>
                  <Badge color={stateColor[l.state]}>{stateLabel[l.state]}</Badge>
                </Td>
                <Td>
                  <span title={formatDateTime(l.expires_at)}>{relativeTime(l.expires_at)}</span>
                </Td>
                <Td className="text-right">
                  {canOperate && (
                    <div className="flex justify-end gap-1">
                      {canWrite && !l.reserved && l.state !== "declined" && (
                        <Button size="sm" variant="ghost" onClick={() => convert.mutate(l.ip)}>
                          In Reservierung umwandeln
                        </Button>
                      )}
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={async () => (await confirm(`Lease ${l.ip} löschen? Die Adresse wird sofort wieder frei.`, { danger: true })) && del.mutate(l.ip)}
                      >
                        Löschen
                      </Button>
                    </div>
                  )}
                </Td>
              </tr>
            ))}
          </Table>
        )}
      </div>
      {total > pageSize && (
        <div className="flex items-center justify-end gap-2 text-sm">
          <span className="text-slate-500">
            {page * pageSize + 1}–{Math.min((page + 1) * pageSize, total)} von {total}
          </span>
          <Button size="sm" disabled={page === 0} onClick={() => setPage(page - 1)}>
            Zurück
          </Button>
          <Button size="sm" disabled={(page + 1) * pageSize >= total} onClick={() => setPage(page + 1)}>
            Weiter
          </Button>
        </div>
      )}
    </div>
  );
}

import { useEffect, useState } from "react";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, errorMessage, unwrap, type S } from "../api/client";
import { useAuth } from "../auth";
import { Badge, Button, Input, PageHeader, Select, Spinner, Table, Td, useFeedback } from "../components/ui";
import { messages } from "../i18n";
import { c } from "../i18n/common";
import { downloadFile, fileStamp } from "../lib/download";
import { formatDateTime, relativeTime, stateLabel, stateLabels } from "../lib/format";
import { useScopes } from "./Scopes";

const m = messages({
  en: {
    title: "Leases",
    subtitle: "Assigned and recently used addresses",
    scope: "Scope",
    allScopes: "All scopes",
    exported: "{n} leases exported",
    reservationCreated: "Reservation for {ip} created",
    deleted: "Lease deleted",
    searchHint: "IP, MAC, hostname, client ID",
    state: "State",
    hostname: "Hostname",
    expires: "Expires",
    reserved: "reserved",
    toReservation: "Convert to reservation",
    confirmDelete: "Delete lease {ip}? The address becomes available again immediately.",
  },
  de: {
    title: "Leases",
    subtitle: "Vergebene und zuletzt genutzte Adressen",
    scope: "Scope",
    allScopes: "Alle Scopes",
    exported: "{n} Leases exportiert",
    reservationCreated: "Reservierung für {ip} angelegt",
    deleted: "Lease gelöscht",
    searchHint: "IP, MAC, Hostname, Client-ID",
    state: "Zustand",
    hostname: "Hostname",
    expires: "Läuft ab",
    reserved: "reserviert",
    toReservation: "In Reservierung umwandeln",
    confirmDelete: "Lease {ip} löschen? Die Adresse wird sofort wieder frei.",
  },
});

export function LeasesPage() {
  const scopes = useScopes();
  const [scopeId, setScopeId] = useState(0);
  return (
    <>
      <PageHeader title={m("title")} subtitle={m("subtitle")} actions={<LeaseExportButton />} />
      <div className="mb-4 max-w-xs">
        <Select label={m("scope")} value={String(scopeId)} onChange={(e) => setScopeId(Number(e.target.value))}>
          <option value="0">{m("allScopes")}</option>
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
          toast(m("exported", { n: data.leases.length }));
        } catch (e) {
          toast(errorMessage(e), "error");
        } finally {
          setBusy(false);
        }
      }}
    >
      {c("export")}
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
      toast(m("reservationCreated", { ip: r.ip }));
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
  const del = useMutation({
    mutationFn: (ip: string) => unwrap(api.DELETE("/leases/{ip}", { params: { path: { ip } } })),
    onSuccess: () => {
      refresh();
      toast(m("deleted"));
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });

  const total = leases.data?.total ?? 0;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-end gap-3">
        <div className="w-full max-w-sm">
          <Input label={c("search")} placeholder={m("searchHint")} value={q} onChange={(e) => setQ(e.target.value)} />
        </div>
        <div className="w-44">
          <Select label={m("state")} value={state} onChange={(e) => setState(e.target.value as S["LeaseState"] | "")}>
            <option value="">{c("all")}</option>
            {stateLabels().map(([k, v]) => (
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
          <Table head={["IP", "MAC", m("hostname"), m("scope"), m("state"), m("expires"), ""]} empty={leases.data?.items.length === 0}>
            {leases.data?.items.map((l) => (
              <tr key={l.ip} data-testid={`lease-${l.ip}`}>
                <Td mono>
                  {l.ip} {l.reserved && <Badge color="teal">{m("reserved")}</Badge>}
                </Td>
                <Td mono>{l.mac}</Td>
                <Td>{l.hostname || "–"}</Td>
                <Td>{l.scope_name}</Td>
                <Td>
                  <Badge color={stateColor[l.state]}>{stateLabel(l.state)}</Badge>
                </Td>
                <Td>
                  <span title={formatDateTime(l.expires_at)}>{relativeTime(l.expires_at)}</span>
                </Td>
                <Td className="text-right">
                  {canOperate && (
                    <div className="flex justify-end gap-1">
                      {canWrite && !l.reserved && l.state !== "declined" && (
                        <Button size="sm" variant="ghost" onClick={() => convert.mutate(l.ip)}>
                          {m("toReservation")}
                        </Button>
                      )}
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={async () => (await confirm(m("confirmDelete", { ip: l.ip }), { danger: true })) && del.mutate(l.ip)}
                      >
                        {c("delete")}
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
          <span className="text-slate-500">{c("page", { from: page * pageSize + 1, to: Math.min((page + 1) * pageSize, total), total })}</span>
          <Button size="sm" disabled={page === 0} onClick={() => setPage(page - 1)}>
            {c("back")}
          </Button>
          <Button size="sm" disabled={(page + 1) * pageSize >= total} onClick={() => setPage(page + 1)}>
            {c("next")}
          </Button>
        </div>
      )}
    </div>
  );
}

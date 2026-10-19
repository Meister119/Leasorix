import { Fragment, useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { api, unwrap } from "../api/client";
import { Badge, Button, Input, PageHeader, Spinner, Table, Td } from "../components/ui";
import { messages } from "../i18n";
import { formatDateTime } from "../lib/format";

const m = messages({
  en: {
    title: "Audit log",
    subtitle: "Who changed what and when – including lease events",
    user: "User",
    userHint: "e.g. admin, dhcp",
    action: "Action (prefix)",
    actionHint: "e.g. lease. or scope.create",
    objectType: "Object type",
    objectTypeHint: "scope, lease, user …",
    filter: "Filter",
    time: "Time",
    source: "Source",
    actionCol: "Action",
    object: "Object",
    hideDetails: "Hide details",
    details: "Details",
    older: "Load older",
  },
  de: {
    title: "Audit-Log",
    subtitle: "Wer hat was wann geändert – inklusive Lease-Ereignissen",
    user: "Benutzer",
    userHint: "z. B. admin, dhcp",
    action: "Aktion (Präfix)",
    actionHint: "z. B. lease. oder scope.create",
    objectType: "Objekttyp",
    objectTypeHint: "scope, lease, user …",
    filter: "Filtern",
    time: "Zeit",
    source: "Quelle",
    actionCol: "Aktion",
    object: "Objekt",
    hideDetails: "Details ausblenden",
    details: "Details",
    older: "Ältere laden",
  },
});

export function AuditPage() {
  const [actor, setActor] = useState("");
  const [action, setAction] = useState("");
  const [objectType, setObjectType] = useState("");
  const [filter, setFilter] = useState({ actor: "", action: "", objectType: "" });
  const [open, setOpen] = useState<number | null>(null);

  const q = useInfiniteQuery({
    queryKey: ["audit", filter],
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET("/audit", {
          params: {
            query: {
              limit: 50,
              ...(pageParam ? { before_id: pageParam } : {}),
              ...(filter.actor ? { actor: filter.actor } : {}),
              ...(filter.action ? { action: filter.action } : {}),
              ...(filter.objectType ? { object_type: filter.objectType } : {}),
            },
          },
        }),
      ),
    getNextPageParam: (last) => last.next_before_id ?? undefined,
  });
  const items = q.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <>
      <PageHeader title={m("title")} subtitle={m("subtitle")} />
      <form
        className="mb-4 grid gap-3 sm:grid-cols-[1fr_1fr_1fr_auto] sm:items-end"
        onSubmit={(e) => {
          e.preventDefault();
          setFilter({ actor, action, objectType });
        }}
      >
        <Input label={m("user")} value={actor} onChange={(e) => setActor(e.target.value)} placeholder={m("userHint")} />
        <Input label={m("action")} value={action} onChange={(e) => setAction(e.target.value)} placeholder={m("actionHint")} />
        <Input label={m("objectType")} value={objectType} onChange={(e) => setObjectType(e.target.value)} placeholder={m("objectTypeHint")} />
        <Button type="submit" variant="primary">
          {m("filter")}
        </Button>
      </form>
      <div className="rounded-lg border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        {q.isLoading ? (
          <Spinner />
        ) : (
          <Table head={[m("time"), m("user"), m("source"), m("actionCol"), m("object"), ""]} empty={items.length === 0}>
            {items.map((e) => (
              <Fragment key={e.id}>
                <tr>
                  <Td>{formatDateTime(e.time)}</Td>
                  <Td>{e.actor}</Td>
                  <Td mono>{e.source ?? "–"}</Td>
                  <Td>
                    <Badge color={e.action.startsWith("lease.") ? "blue" : e.action.startsWith("auth.") ? "amber" : "teal"}>{e.action}</Badge>
                  </Td>
                  <Td mono>
                    {e.object_type}/{e.object_id}
                  </Td>
                  <Td className="text-right">
                    {e.details && (
                      <Button size="sm" variant="ghost" onClick={() => setOpen(open === e.id ? null : e.id)}>
                        {open === e.id ? m("hideDetails") : m("details")}
                      </Button>
                    )}
                  </Td>
                </tr>
                {open === e.id && (
                  <tr>
                    <td colSpan={6} className="bg-slate-50 px-3 py-2 dark:bg-slate-950">
                      <pre className="overflow-x-auto font-mono text-xs">{JSON.stringify(e.details, null, 2)}</pre>
                    </td>
                  </tr>
                )}
              </Fragment>
            ))}
          </Table>
        )}
      </div>
      {q.hasNextPage && (
        <div className="mt-4 flex justify-center">
          <Button onClick={() => q.fetchNextPage()} disabled={q.isFetchingNextPage}>
            {m("older")}
          </Button>
        </div>
      )}
    </>
  );
}

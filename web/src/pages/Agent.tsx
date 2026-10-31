import { useCallback, useEffect, useState } from "react";

import { AgentPanel } from "@/components/AgentPanel";
import { Hotkey, Shell } from "@/components/Shell";
import { Panel } from "@/components/ui/panel";
import { type AgentAction, agentApi } from "@/lib/agent";
import { api, type ServerView } from "@/lib/api";
import { href, navigate, type Route } from "@/lib/router";
import { cn } from "@/lib/utils";

const stateLabel: Record<AgentAction["state"], string> = {
  pending: "ждёт",
  approved: "выполняется",
  rejected: "отклонено",
  expired: "истекло",
  done: "выполнено",
  failed: "ошибка",
};

// Journal of commands the agent asked to run with confirmation.
function ActionsLog() {
  const [list, setList] = useState<AgentAction[] | null>(null);
  const [open, setOpen] = useState<number | null>(null);
  const load = useCallback(() => {
    agentApi
      .actions()
      .then(setList)
      .catch(() => setList([]));
  }, []);
  useEffect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, [load]);
  return (
    <Panel title="журнал подтверждений" className="flex min-h-0 flex-col">
      <div className="min-h-0 flex-1 overflow-y-auto pr-1 [scrollbar-width:thin]">
        {list === null && <div className="text-muted-foreground">загрузка…</div>}
        {list?.length === 0 && (
          <div className="text-xs text-muted-foreground">
            Здесь появятся команды, которые агент выполнял после вашего подтверждения. Чтение (логи, docker ps, df…)
            выполняется без подтверждения и пишется в аудит.
          </div>
        )}
        {list?.map((a) => (
          <div key={a.id} className="border-t border-border/60 py-1.5 text-xs first:border-t-0">
            <button
              type="button"
              className="flex w-full items-center gap-2 text-left"
              onClick={() => setOpen(open === a.id ? null : a.id)}
            >
              <span
                className={cn(
                  "w-20 shrink-0",
                  a.state === "done"
                    ? "text-primary"
                    : a.state === "pending" || a.state === "approved"
                      ? "text-warn"
                      : "text-crit",
                )}
              >
                {stateLabel[a.state]}
              </span>
              <span className="shrink-0 text-hi">{a.server}</span>
              <span className="min-w-0 flex-1 truncate text-muted-foreground">{a.command}</span>
              <span className="shrink-0 text-muted-foreground">{a.created_at.slice(5, 16).replace("T", " ")}</span>
            </button>
            {open === a.id && (
              <div className="mt-1 pl-22">
                {a.reason && <div className="text-muted-foreground">{a.reason}</div>}
                <pre className="mt-1 whitespace-pre-wrap break-all bg-field px-2 py-1 text-hi">{a.command}</pre>
                {a.decided_by && <div className="mt-1 text-muted-foreground">решение: {a.decided_by}</div>}
                {a.exit_code !== undefined && <div className="text-muted-foreground">код выхода {a.exit_code}</div>}
                {a.output && (
                  <pre className="mt-1 max-h-60 overflow-auto bg-field px-2 py-1 text-foreground [scrollbar-width:thin]">
                    {a.output}
                  </pre>
                )}
              </div>
            )}
          </div>
        ))}
      </div>
    </Panel>
  );
}

export function AgentPage({
  serverId,
  route,
  version,
  onLogout,
}: {
  serverId?: number;
  route: Route;
  version: string;
  onLogout: () => void;
}) {
  const [servers, setServers] = useState<ServerView[]>([]);
  useEffect(() => {
    api
      .servers()
      .then(setServers)
      .catch((e) => e?.status === 401 && onLogout());
  }, [onLogout]);
  const ctx = servers.find((s) => s.id === serverId);

  return (
    <Shell
      route={route}
      version={version}
      onLogout={onLogout}
      footer={
        <>
          <Hotkey k="3">терминал</Hotkey>
          <span className="text-muted-foreground">агент работает через srvmon-gateway, ключей у него нет</span>
        </>
      }
      footerNote="изменения на серверах — только после вашего подтверждения"
    >
      <div className="flex items-center gap-3 text-xs">
        <label htmlFor="agent-ctx" className="text-muted-foreground">
          контекст:
        </label>
        <select
          id="agent-ctx"
          className="h-7 border border-border bg-field px-2 text-hi"
          value={serverId ?? 0}
          onChange={(e) => navigate(href.agent(Number(e.target.value) || undefined))}
        >
          <option value={0}>все серверы</option>
          {servers.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </select>
      </div>
      <div className="flex min-h-0 flex-1 flex-col gap-4 lg:flex-row">
        <AgentPanel
          context={ctx ? { id: ctx.id, name: ctx.name } : undefined}
          className="h-[600px] min-w-0 lg:h-[calc(100vh-10rem)] lg:flex-1"
        />
        <div className="flex min-h-0 flex-col lg:h-[calc(100vh-10rem)] lg:w-[460px] lg:shrink-0">
          <ActionsLog />
        </div>
      </div>
    </Shell>
  );
}

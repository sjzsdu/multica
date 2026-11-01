"use client";

import { useMemo, useState } from "react";
import { isRuntimeUsableForUser } from "@multica/core/runtimes";
import { ArrowDown, ArrowUp, Monitor, Plus, Trash2 } from "@multica/ui/icons";
import type {
  AgentFallbackRuntimeEntry,
  AgentRuntime,
} from "@multica/core/types";
import {
  PickerItem,
  PropertyPicker,
} from "../../../issues/components/pickers";
import { ProviderLogo } from "../../../runtimes/components/provider-logo";
import { runtimeRowLabel } from "../../../runtimes/components/runtime-machines";
import { useT } from "../../../i18n";
import { ModelPicker } from "./model-picker";

const ROW_CLASS =
  "flex items-center gap-2 rounded-lg border border-input bg-input/30 px-2.5 py-1.5";

/**
 * Ordered fallback chain editor for the agent's Execution settings. Each row
 * is one (runtime + model) entry the server tries — top to bottom — when the
 * primary runtime comes back Blocked. Every edit commits the whole chain
 * through `onChange` immediately: the API has no per-entry endpoints and the
 * write-time validator (dedupe, no fallback == primary) only accepts a
 * complete, valid list, so there is deliberately no local draft state. The
 * add picker only offers runtimes that would pass that validator.
 *
 * Per-entry `thinking_level` / `service_tier` overrides are preserved
 * transparently (round-tripped untouched) but have no editor yet; a chain
 * entry without a model runs the runtime's own default.
 */
export function FallbackRuntimesField({
  value,
  primaryRuntimeId,
  runtimes,
  currentUserId,
  canEdit,
  onChange,
}: {
  value: AgentFallbackRuntimeEntry[];
  primaryRuntimeId: string | null;
  runtimes: AgentRuntime[];
  currentUserId: string | null;
  /** When false, render a static read-only chain with no editing controls. */
  canEdit: boolean;
  onChange: (entries: AgentFallbackRuntimeEntry[]) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  const [addOpen, setAddOpen] = useState(false);

  const entries = useMemo(() => value ?? [], [value]);
  const chainIds = useMemo(
    () => new Set(entries.map((entry) => entry.runtime_id)),
    [entries],
  );
  const runtimeById = useMemo(
    () => new Map(runtimes.map((rt) => [rt.id, rt])),
    [runtimes],
  );

  // Mirrors the server's write validation exactly: a candidate must not be
  // the primary, must not already be chained, and must be usable by the
  // current user — the same predicate the primary RuntimePicker locks with,
  // so the picker never offers an entry the API would reject.
  const eligible = useMemo(
    () =>
      runtimes.filter(
        (rt) =>
          rt.id !== primaryRuntimeId &&
          !chainIds.has(rt.id) &&
          isRuntimeUsableForUser(rt, currentUserId),
      ),
    [runtimes, primaryRuntimeId, chainIds, currentUserId],
  );

  const commit = (next: AgentFallbackRuntimeEntry[]) => {
    // Empty model string means "use the runtime's own default"; the wire
    // type omits it. thinking_level / service_tier ride along untouched.
    void onChange(
      next.map((entry) => ({
        runtime_id: entry.runtime_id,
        ...(entry.model ? { model: entry.model } : {}),
        ...(entry.thinking_level ? { thinking_level: entry.thinking_level } : {}),
        ...(entry.service_tier ? { service_tier: entry.service_tier } : {}),
      })),
    );
  };

  const add = (runtimeId: string) => {
    setAddOpen(false);
    if (chainIds.has(runtimeId) || runtimeId === primaryRuntimeId) return;
    commit([...entries, { runtime_id: runtimeId }]);
  };

  const setModel = (index: number, model: string) => {
    commit(entries.map((entry, i) => (i === index ? { ...entry, model } : entry)));
  };

  const remove = (index: number) => {
    commit(entries.filter((_, i) => i !== index));
  };

  const move = (index: number, delta: -1 | 1) => {
    const target = index + delta;
    if (target < 0 || target >= entries.length) return;
    const next = [...entries];
    const [entry] = next.splice(index, 1);
    if (!entry) return;
    next.splice(target, 0, entry);
    commit(next);
  };

  return (
    <div className="w-full max-w-md space-y-2">
      {entries.length === 0 ? (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.inspector.fallback_empty)}
        </p>
      ) : (
        <ul className="space-y-1.5" aria-label={t(($) => $.inspector.prop_fallback_runtimes)}>
          {entries.map((entry, index) => {
            const runtime = runtimeById.get(entry.runtime_id) ?? null;
            const online = runtime?.status === "online";
            const label = runtime ? runtimeRowLabel(runtime, "") : entry.runtime_id;
            return (
              <li key={`${entry.runtime_id}-${index}`} className={ROW_CLASS}>
                <span
                  className="flex h-5 w-5 shrink-0 items-center justify-center rounded-xs bg-muted text-micro font-medium tabular-nums text-muted-foreground"
                  aria-hidden="true"
                >
                  {index + 1}
                </span>
                {runtime ? (
                  <ProviderLogo
                    provider={runtime.provider}
                    className="h-4 w-4 shrink-0"
                  />
                ) : (
                  <Monitor
                    className="h-4 w-4 shrink-0 text-muted-foreground"
                    aria-hidden="true"
                  />
                )}
                <span
                  className="min-w-0 flex-1 truncate text-caption"
                  title={label}
                >
                  {label}
                </span>
                {runtime ? (
                  <span
                    className={`h-1.5 w-1.5 shrink-0 rounded-full ${
                      online ? "bg-success" : "bg-muted-foreground/40"
                    }`}
                    aria-hidden="true"
                  />
                ) : null}
                <ModelPicker
                  variant="chip"
                  runtimeId={entry.runtime_id}
                  runtimeOnline={online}
                  value={entry.model ?? ""}
                  canEdit={canEdit}
                  onChange={(model) => setModel(index, model)}
                />
                {canEdit ? (
                  <span className="flex shrink-0 items-center gap-0.5">
                    <button
                      type="button"
                      onClick={() => move(index, -1)}
                      disabled={index === 0}
                      aria-label={t(($) => $.inspector.fallback_move_up_aria, {
                        index: index + 1,
                      })}
                      className="rounded-xs p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground disabled:pointer-events-none disabled:opacity-40"
                    >
                      <ArrowUp className="h-3.5 w-3.5" />
                    </button>
                    <button
                      type="button"
                      onClick={() => move(index, 1)}
                      disabled={index === entries.length - 1}
                      aria-label={t(($) => $.inspector.fallback_move_down_aria, {
                        index: index + 1,
                      })}
                      className="rounded-xs p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground disabled:pointer-events-none disabled:opacity-40"
                    >
                      <ArrowDown className="h-3.5 w-3.5" />
                    </button>
                    <button
                      type="button"
                      onClick={() => remove(index)}
                      aria-label={t(($) => $.inspector.fallback_remove_aria, {
                        index: index + 1,
                      })}
                      className="rounded-xs p-1 text-muted-foreground transition-colors hover:bg-destructive/10 hover:text-destructive"
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </button>
                  </span>
                ) : null}
              </li>
            );
          })}
        </ul>
      )}

      {canEdit ? (
        <PropertyPicker
          open={addOpen}
          onOpenChange={setAddOpen}
          width="w-72"
          align="start"
          triggerRender={
            <button
              type="button"
              aria-label={t(($) => $.inspector.fallback_add)}
              className="flex items-center gap-1.5 rounded-lg border border-dashed border-input px-2.5 py-1.5 text-caption text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
            />
          }
          trigger={
            <>
              <Plus className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
              {t(($) => $.inspector.fallback_add)}
            </>
          }
        >
          {eligible.length === 0 ? (
            <p className="px-2 py-3 text-center text-caption text-muted-foreground">
              {t(($) => $.inspector.fallback_add_empty)}
            </p>
          ) : (
            eligible.map((rt) => {
              const online = rt.status === "online";
              return (
                <PickerItem
                  key={rt.id}
                  selected={false}
                  onClick={() => add(rt.id)}
                >
                  <ProviderLogo
                    provider={rt.provider}
                    className="h-4 w-4 shrink-0"
                  />
                  <span className="min-w-0 flex-1 truncate text-caption">
                    {runtimeRowLabel(rt, "")}
                  </span>
                  <span
                    className={`h-1.5 w-1.5 shrink-0 rounded-full ${
                      online ? "bg-success" : "bg-muted-foreground/40"
                    }`}
                    aria-label={online ? t(($) => $.pickers.runtime_online) : t(($) => $.pickers.runtime_offline)}
                  />
                </PickerItem>
              );
            })
          )}
        </PropertyPicker>
      ) : null}
    </div>
  );
}

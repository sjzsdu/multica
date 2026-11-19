"use client";

import { useCallback } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentMember } from "@multica/core/permissions";
import { runtimeListOptions } from "@multica/core/runtimes";
import type { AgentFallbackRuntimeEntry } from "@multica/core/types";
import {
  systemFallbackRuntimeOptions,
  workspaceKeys,
} from "@multica/core/workspace/queries";
import { useT } from "../../i18n";
import { FallbackRuntimesField } from "../../agents/components/inspector/fallback-runtimes-field";
import { SettingsCard, SettingsSection, SettingsTab } from "./settings-layout";

/**
 * The instance-wide last-resort runtime (system_config.settings.
 * fallback_runtime): the chain's final tier, tried only when an agent's
 * primary runtime and its own fallback chain are both Blocked. It applies to
 * every workspace, while the runtime itself is picked from the current
 * workspace — the same inventory the agent-level pickers use, so the entry
 * already passes write-time validation.
 *
 * The editor is capped at one entry: the chain has a single global tier.
 */
export function SystemFallbackRuntimeTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const currentUser = useAuthStore((s) => s.user);
  const { role } = useCurrentMember(wsId);
  const canEdit = role === "owner" || role === "admin";

  const { data: entry, isPending, isError } = useQuery(
    systemFallbackRuntimeOptions(wsId),
  );
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));

  const save = useCallback(
    async (entries: AgentFallbackRuntimeEntry[]) => {
      if (!canEdit) return;
      try {
        const next = await api.updateSystemFallbackRuntime(entries[0] ?? null);
        qc.setQueryData(workspaceKeys.systemFallbackRuntime(wsId), next);
        toast.success(t(($) => $.fallback_runtime.saved));
      } catch (e) {
        toast.error(
          e instanceof Error ? e.message : t(($) => $.fallback_runtime.save_error),
        );
      }
    },
    [canEdit, qc, t, wsId],
  );

  return (
    <SettingsTab
      title={t(($) => $.page.tabs.fallback_runtime)}
      description={t(($) => $.fallback_runtime.description)}
      scope="workspace"
    >
      <SettingsSection anchor="runtime" title={t(($) => $.fallback_runtime.title)}>
        <SettingsCard>
          <div className="space-y-2 px-4 py-3.5">
            {isPending ? null : (
              <FallbackRuntimesField
                value={entry ? [entry] : []}
                primaryRuntimeId={null}
                runtimes={runtimes}
                currentUserId={currentUser?.id ?? null}
                canEdit={canEdit}
                maxEntries={1}
                emptyText={t(($) => $.fallback_runtime.empty)}
                onChange={save}
              />
            )}
            <p className="text-caption text-muted-foreground">
              {t(($) => $.fallback_runtime.hint)}
            </p>
          </div>
        </SettingsCard>
        {!canEdit ? (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.fallback_runtime.admin_only)}
          </p>
        ) : null}
        {isError ? (
          <p role="alert" className="text-caption text-destructive">
            {t(($) => $.fallback_runtime.load_error)}
          </p>
        ) : null}
      </SettingsSection>
    </SettingsTab>
  );
}

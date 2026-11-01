"use client";

import { useCallback, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import type { Agent, AgentRuntime } from "@multica/core/types";
import {
  isRuntimeUsableForUser,
  runtimeModelsOptions,
} from "@multica/core/runtimes";
import { buildModelChangeUpdate, type ModelCatalog } from "./model-change-cleanup";

/**
 * Runtime and model editing shared by the properties panel and the General
 * settings form, so both write the same PATCH for the same change.
 */
export function useExecutionControls({
  agent,
  runtime,
  runtimes,
  currentUserId,
  update,
}: {
  agent: Agent;
  runtime: AgentRuntime | null;
  runtimes: AgentRuntime[];
  currentUserId: string | null;
  update: (data: Record<string, unknown>) => Promise<void>;
}) {
  // A Qoder agent is bound to an agent in Qoder Cloud Agents, and the model lives there. Moving
  // between Qoder and any other runtime would need a different configuration
  // the server either rejects or the bridge refuses, so neither direction is
  // offered.
  const qoderRuntime = runtime?.provider === "qoder_cloud";
  const isOnline = runtime?.status === "online";
  const canReadRuntime =
    runtime != null && isRuntimeUsableForUser(runtime, currentUserId);
  const canDiscoverRuntimeModels = isOnline && canReadRuntime && !qoderRuntime;
  const runtimeOptions = useMemo(
    () =>
      qoderRuntime
        ? runtimes
        : runtimes.filter((r) => r.provider !== "qoder_cloud"),
    [qoderRuntime, runtimes],
  );

  // Same query the Thinking / Speed fields already use, so switching model
  // costs no extra request. `null` = not authoritative (offline runtime, still
  // loading, or discovery failed) and must not trigger any clearing.
  const modelsQuery = useQuery(
    runtimeModelsOptions(canDiscoverRuntimeModels ? agent.runtime_id : null),
  );
  const modelCatalog = useMemo<ModelCatalog>(
    () =>
      modelsQuery.isSuccess
        ? modelsQuery.data.supported
          ? modelsQuery.data.models
          : []
        : null,
    [modelsQuery.data, modelsQuery.isSuccess],
  );

  const changeModel = useCallback(
    (model: string) =>
      update(
        buildModelChangeUpdate({
          provider: runtime?.provider ?? "",
          model,
          thinkingLevel: agent.thinking_level ?? "",
          serviceTier: agent.service_tier ?? "",
          catalog: modelCatalog,
        }),
      ),
    [agent.service_tier, agent.thinking_level, modelCatalog, runtime?.provider, update],
  );

  // Model, thinking level, and service tier are runtime/model native. Clear
  // them together so the new runtime resolves its own defaults instead of
  // inheriting incompatible tokens. The write validator rejects a chain entry
  // naming the new primary, so strip it in the same request — switching to a
  // runtime that was previously a fallback never 400s; the remaining chain
  // still applies.
  const changeRuntime = useCallback(
    (runtimeId: string) => {
      // Switching runtimes invalidates runtime-native model settings, and any
      // fallback entry pointing at the NEW runtime (it would shadow the
      // primary). Omit the key entirely for agents without fallbacks so the
      // PATCH stays identical to the pre-fallback shape.
      const remainingFallbacks = (agent.fallback_runtimes ?? []).filter(
        (entry) => entry.runtime_id !== runtimeId,
      );
      return update({
        runtime_id: runtimeId,
        model: "",
        thinking_level: "",
        service_tier: "",
        ...(agent.fallback_runtimes?.length
          ? { fallback_runtimes: remainingFallbacks }
          : {}),
      });
    },
    [agent.fallback_runtimes, update],
  );

  return {
    qoderRuntime,
    canDiscoverRuntimeModels,
    runtimeOptions,
    changeModel,
    changeRuntime,
  };
}

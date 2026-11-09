import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import type { Agent, UpdateAgentRequest } from "../types";
import { cacheAgentResponse, workspaceKeys } from "../workspace/queries";

export interface UpdateAgentInput {
  id: string;
  data: Record<string, unknown>;
}

interface UpdateAgentContext {
  prevListAgent?: Agent;
  prevDetailAgent?: Agent;
  prevListFields?: Record<string, unknown>;
  prevDetailFields?: Record<string, unknown>;
}

/**
 * Shared optimistic agent-update mutation: the agents list inline editors
 * (runtime / model cells) and the detail inspector all write through it so
 * there is exactly one implementation of the cache rules.
 *
 * Optimistic before the round-trip so a picker selection flips immediately.
 * On error it rolls back ONLY the fields this call wrote (never a
 * whole-entity snapshot), so a concurrently succeeding mutation of different
 * fields on the same agent is not clobbered by the rollback. The settle-time
 * invalidation then converges both caches with the server. It deliberately
 * does not toast — callers own their feedback surface.
 */
export function useUpdateAgent() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation<Agent, Error, UpdateAgentInput, UpdateAgentContext>({
    mutationFn: ({ id, data }) =>
      api.updateAgent(id, data as UpdateAgentRequest),
    onMutate: async ({ id, data }) => {
      // A runtime rebind derives runtime_bound locally so the "Needs a
      // runtime" flag in the list flips together with the field it reads.
      const optimisticData =
        typeof data.runtime_id === "string"
          ? { ...data, runtime_bound: data.runtime_id.trim().length > 0 }
          : data;
      const listKey = workspaceKeys.agents(wsId);
      const detailKey = workspaceKeys.agent(wsId, id);
      // Cancel in-flight refetches so a stale response can't land after the
      // optimistic write and visually revert it.
      await qc.cancelQueries({ queryKey: listKey });
      const prevListAgent = qc
        .getQueryData<Agent[]>(listKey)
        ?.find((agent) => agent.id === id);
      const prevDetailAgent = qc.getQueryData<Agent>(detailKey);
      const previousFields = (agent: Agent | undefined) => {
        const fields: Record<string, unknown> = {};
        if (!agent) return fields;
        for (const key of Object.keys(optimisticData)) {
          fields[key] = (agent as unknown as Record<string, unknown>)[key];
        }
        return fields;
      };
      qc.setQueryData<Agent[]>(listKey, (old) =>
        old?.map((agent) =>
          agent.id === id
            ? ({ ...agent, ...optimisticData } as Agent)
            : agent,
        ),
      );
      qc.setQueryData<Agent>(detailKey, (old) =>
        old ? ({ ...old, ...optimisticData } as Agent) : old,
      );
      return {
        prevListAgent,
        prevDetailAgent,
        prevListFields: previousFields(prevListAgent),
        prevDetailFields: previousFields(prevDetailAgent),
      };
    },
    // onSettled still runs after this, so the caches converge even when a
    // rollback raced a newer server state.
    onError: (_error, { id }, context) => {
      if (!context) return;
      const { prevListAgent, prevDetailAgent } = context;
      if (prevListAgent) {
        const fields = context.prevListFields ?? {};
        qc.setQueryData<Agent[]>(workspaceKeys.agents(wsId), (old) =>
          old?.map((agent) =>
            agent.id === id ? ({ ...agent, ...fields } as Agent) : agent,
          ),
        );
      }
      if (prevDetailAgent) {
        const fields = context.prevDetailFields ?? {};
        qc.setQueryData<Agent>(workspaceKeys.agent(wsId, id), (old) =>
          old ? ({ ...old, ...fields } as Agent) : old,
        );
      }
    },
    onSuccess: (updated) => {
      // The authoritative response patches both caches immediately; the
      // list stays a projection of the same entity without waiting for the
      // invalidate round-trip.
      cacheAgentResponse(qc, wsId, updated, { insertIntoList: false });
    },
    onSettled: () => {
      // Prefix match: the detail key lives under the list key, so this
      // refetches both.
      void qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
    },
  });
}

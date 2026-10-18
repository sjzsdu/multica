import React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import { toast } from "sonner";
import type {
  Agent,
  AgentRuntime,
  RuntimeModelsResult,
} from "@multica/core/types";
import type { AgentActivity } from "@multica/core/agents";
import type { SupportedLocale } from "@multica/core/i18n";
import { renderWithI18n } from "../../test/i18n";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import { AgentsPage } from "./agents-page";

// These tests pin the `listReady` render gate (MUL-4511): the Agents list must
// not paint real rows until the auxiliary queries the active sort field /
// filter depends on have landed, or it sorts on placeholder values
// (lastActiveDays null→Infinity, runCount 0) and visibly re-orders when each
// query resolves. The gate waits per need — nothing for name/created,
// run-counts for runs, activity + run-counts for the default lastActive,
// presence when an availability filter is active — and never blocks the empty
// state on those queries.

const mocks = vi.hoisted(() => ({
  agents: [] as Agent[],
  agentsLoading: false,
  runCounts: [] as Array<{ agent_id: string; run_count: number }>,
  runCountsPending: false,
  activity: {
    byAgent: new Map<string, AgentActivity>(),
    loading: false,
  },
  presence: {
    byAgent: new Map<string, unknown>(),
    loading: false,
  },
  runtimes: [] as AgentRuntime[],
  runtimeModels: {
    models: [],
    unavailableModels: [],
    supported: true,
  } as RuntimeModelsResult,
  updateAgentMutate: vi.fn(),
  viewState: {
    scope: "all",
    sortField: "lastActive" as string,
    sortDirection: "desc" as string,
    hiddenColumns: ["created"] as string[],
    filters: {
      availability: [] as string[],
      runtimes: [] as string[],
      owners: [] as string[],
      models: [] as string[],
      access: [] as string[],
    },
    setScope: vi.fn(),
    toggleSort: vi.fn(),
    setSortField: vi.fn(),
    setSortDirection: vi.fn(),
    toggleColumn: vi.fn(),
    toggleFilter: vi.fn(),
    clearFilters: vi.fn(),
  },
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { queryKey?: readonly unknown[] }) => {
    const key = options.queryKey?.[0];
    if (key === "agents") {
      return {
        data: mocks.agents,
        isLoading: mocks.agentsLoading,
        error: null,
        refetch: vi.fn(),
      };
    }
    if (key === "agent-run-counts") {
      return { data: mocks.runCounts, isPending: mocks.runCountsPending };
    }
    if (key === "runtimes") {
      return { data: mocks.runtimes, isLoading: false, isPending: false };
    }
    if (key === "runtime-models") {
      return {
        data: mocks.runtimeModels,
        isSuccess: true,
        isLoading: false,
        isPending: false,
      };
    }
    return { data: [], isLoading: false, isPending: false };
  },
  // Anything else building a mutation observer in this tree; the inline
  // cells go through the mocked agents/mutations hook below.
  useMutation: () => ({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
}));

// Inline runtime/model cells write through the shared optimistic hook;
// capture the mutate calls here. The hook's cache rules (optimistic patch,
// field-scoped rollback, settle-time invalidation) run for real and are
// pinned in agent-detail-page.test.
vi.mock("@multica/core/agents/mutations", () => ({
  useUpdateAgent: () => ({
    mutate: mocks.updateAgentMutate,
    isPending: false,
  }),
}));

// The list virtualizes; render every row so DOM order reflects sort order.
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: count }, (_, index) => ({
        index,
        key: index,
        start: index * 64,
        end: (index + 1) * 64,
        size: 64,
      })),
    getTotalSize: () => count * 64,
  }),
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

vi.mock("@multica/core/agents", () => ({
  isAgentRuntimeBound: (agent: { runtime_id: string; runtime_bound?: boolean }) =>
    agent.runtime_bound !== false && agent.runtime_id.length > 0,
  agentRunCounts30dOptions: () => ({ queryKey: ["agent-run-counts"] }),
  useWorkspaceActivityMap: () => mocks.activity,
  useWorkspacePresenceMap: () => mocks.presence,
  VISIBILITY_TOOLTIP: { private: "Private", workspace: "Workspace" },
  effectiveAccessScope: (pm: unknown, it: unknown) => {
    if (pm !== "public_to") return "owner-only";
    if ((Array.isArray(it) ? it : []).some((t) => (t as {target_type?: string})?.target_type === "workspace")) return "workspace";
    return "specific-people";
  },
  ALL_ACCESS_SCOPES: ["workspace", "specific-people", "owner-only"],
}));

vi.mock("@multica/core/agents/stores", () => ({
  useAgentsViewStore: (selector: (state: unknown) => unknown) =>
    selector(mocks.viewState),
  AGENT_DEFAULT_HIDDEN_COLUMNS: ["created"],
  AGENT_SCOPES: ["mine", "all", "archived"],
}));

vi.mock("@multica/core/api", () => ({
  api: { archiveAgent: vi.fn(), restoreAgent: vi.fn() },
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: unknown) => unknown) =>
    selector({ user: { id: "user-1" } }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    newAgent: () => "/test-workspace/agents/new",
    newAgentManual: () => "/test-workspace/agents/new/manual",
    agentDetail: (id: string) => `/test-workspace/agents/${id}`,
  }),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  agentListOptions: () => ({ queryKey: ["agents"] }),
  memberListOptions: () => ({ queryKey: ["members"] }),
  workspaceKeys: { agents: (wsId: string) => ["agents", wsId] },
}));

vi.mock("@multica/core/runtimes", async (importOriginal) => ({
  // Partial mock: the runtime picker's machine builder pulls helpers this
  // test doesn't care about (e.g. deriveRuntimeHealth); override only the
  // query hooks and labels the tests pin.
  ...(await importOriginal<typeof import("@multica/core/runtimes")>()),
  runtimeListOptions: () => ({ queryKey: ["runtimes"] }),
  runtimeModelsOptions: (runtimeId: string | null) => ({
    queryKey: ["runtime-models", runtimeId],
  }),
  refreshRuntimeModels: vi.fn(),
  isRuntimeUsableForUser: () => true,
  runtimeDisplayLabel: (runtime: { name: string }) => runtime.name,
}));

// View-layer children with heavy / portal deps — stub to keep the test focused
// on the gate, not on avatars, row menus, the toolbar, or tooltip portals.
vi.mock("../../common/actor-avatar", () => ({ ActorAvatar: () => null }));
vi.mock("./agent-row-actions", () => ({ AgentRowActions: () => null }));
vi.mock("./agent-list-toolbar", () => ({
  AgentListToolbar: () => <div data-testid="agent-list-toolbar" />,
  countActiveFilterDimensions: () => 0,
}));
vi.mock("../presence", () => ({ availabilityConfig: {} }));
vi.mock("@multica/ui/components/ui/skeleton", () => ({
  Skeleton: (props: Record<string, unknown>) => (
    <div data-testid="skeleton" {...props} />
  ),
}));
vi.mock("@multica/ui/components/ui/tooltip", () => ({
  Tooltip: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  TooltipTrigger: ({ render }: { render: React.ReactNode }) => <>{render}</>,
  TooltipContent: ({ children }: { children: React.ReactNode }) => (
    <div role="tooltip">{children}</div>
  ),
}));

const BASE_AGENT: Agent = {
  id: "agent-base",
  workspace_id: "workspace-1",
  runtime_id: "runtime-1",
  name: "Base Agent",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "cloud",
  runtime_config: {},
  custom_args: [],
  visibility: "workspace",
  permission_mode: "private",
  invocation_targets: [],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "claude",
  owner_id: "user-1",
  skills: [],
  created_at: "2026-06-01T00:00:00Z",
  updated_at: "2026-06-01T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

function makeAgent(over: Partial<Agent>): Agent {
  return { ...BASE_AGENT, ...over };
}

// Build a 30-bucket activity series whose most-recent bucket with runs is
// `daysAgo` days back — `lastActiveDaysAgo` reads exactly this.
function activityLastActive(daysAgo: number): AgentActivity {
  const buckets = Array.from({ length: 30 }, () => ({ total: 0, failed: 0, completed: 0, cancelled: 0 }));
  buckets[29 - daysAgo] = { total: 1, failed: 0, completed: 1, cancelled: 0 };
  return { buckets, daysSinceCreated: 30 };
}

const ALPHA = makeAgent({ id: "a-alpha", name: "Alpha Agent" });
const BETA = makeAgent({ id: "a-beta", name: "Beta Agent" });

// One machine with two runtimes: rebinding needs a different runtime to
// select, and `BASE_AGENT.runtime_id` points at the Claude one.
const RT_CLAUDE: AgentRuntime = {
  id: "runtime-1",
  workspace_id: "workspace-1",
  daemon_id: "daemon-1",
  name: "Claude (mbp.local)",
  runtime_mode: "local",
  provider: "claude",
  launch_header: "",
  status: "online",
  device_info: "mbp.local · macOS (arm64)",
  metadata: {},
  owner_id: "user-1",
  visibility: "public",
  last_seen_at: "2026-09-30T00:00:00Z",
  created_at: "2026-06-01T00:00:00Z",
  updated_at: "2026-06-01T00:00:00Z",
};
const RT_CODEX: AgentRuntime = {
  ...RT_CLAUDE,
  id: "runtime-2",
  name: "Codex (mbp.local)",
  provider: "codex",
};

function makeAdapter(
  overrides: Partial<NavigationAdapter> = {},
): NavigationAdapter {
  return {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/test-workspace/agents",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (p) => p,
    ...overrides,
  };
}

function renderPage(
  locale?: SupportedLocale,
  adapter: NavigationAdapter = makeAdapter(),
) {
  renderWithI18n(
    <NavigationProvider value={adapter}>
      <AgentsPage />
    </NavigationProvider>,
    { locale },
  );
  return adapter;
}

/** Beta before Alpha in document order? */
function betaPrecedesAlpha(): boolean {
  const alpha = screen.getByText("Alpha Agent");
  const beta = screen.getByText("Beta Agent");
  return Boolean(
    beta.compareDocumentPosition(alpha) & Node.DOCUMENT_POSITION_FOLLOWING,
  );
}

beforeEach(() => {
  mocks.agents = [ALPHA, BETA];
  mocks.agentsLoading = false;
  mocks.runCounts = [];
  mocks.runCountsPending = false;
  mocks.activity = { byAgent: new Map(), loading: false };
  mocks.presence = { byAgent: new Map(), loading: false };
  mocks.runtimes = [];
  mocks.runtimeModels = {
    models: [],
    unavailableModels: [],
    supported: true,
  };
  mocks.updateAgentMutate.mockReset();
  mocks.viewState.scope = "all";
  mocks.viewState.sortField = "lastActive";
  mocks.viewState.sortDirection = "desc";
  mocks.viewState.hiddenColumns = ["created"];
  mocks.viewState.filters = {
    availability: [],
    runtimes: [],
    owners: [],
    models: [],
    access: [],
  };
});

describe("AgentsPage listReady gate", () => {
  it("shows only a skeleton (no real rows) while lastActive deps are pending", () => {
    // Default lastActive sort depends on activity + run-counts.
    mocks.activity = { byAgent: new Map(), loading: true };
    mocks.runCountsPending = true;

    renderPage();

    expect(screen.queryByText("Alpha Agent")).not.toBeInTheDocument();
    expect(screen.queryByText("Beta Agent")).not.toBeInTheDocument();
    expect(screen.getAllByTestId("skeleton").length).toBeGreaterThan(0);
  });

  it("renders rows in the resolved lastActive order once deps land", () => {
    // Alpha active 5d ago, Beta active today → lastActive desc puts Beta first,
    // the opposite of the name-order fallback the ungated list would show.
    mocks.activity = {
      byAgent: new Map<string, AgentActivity>([
        [ALPHA.id, activityLastActive(5)],
        [BETA.id, activityLastActive(0)],
      ]),
      loading: false,
    };
    mocks.runCounts = [
      { agent_id: ALPHA.id, run_count: 0 },
      { agent_id: BETA.id, run_count: 0 },
    ];
    mocks.runCountsPending = false;

    renderPage();

    expect(screen.getByText("Alpha Agent")).toBeInTheDocument();
    expect(screen.getByText("Beta Agent")).toBeInTheDocument();
    expect(betaPrecedesAlpha()).toBe(true);
  });

  it("renders rows immediately for name sort without waiting on activity/run-counts", () => {
    mocks.viewState.sortField = "name";
    mocks.viewState.sortDirection = "asc";
    // Auxiliary queries are still in flight — name sort must not wait on them.
    mocks.activity = { byAgent: new Map(), loading: true };
    mocks.runCountsPending = true;
    mocks.presence = { byAgent: new Map(), loading: true };

    renderPage();

    expect(screen.getByText("Alpha Agent")).toBeInTheDocument();
    expect(screen.getByText("Beta Agent")).toBeInTheDocument();
    // name asc → Alpha before Beta.
    expect(betaPrecedesAlpha()).toBe(false);
  });

  it("shows a skeleton (not a false empty/false result) while an availability filter waits on presence", () => {
    // Availability filter needs presence; sort by name so ONLY presence gates.
    // Ungated, presence-null rows would all be filtered out → a false "no
    // matches" state. Gated, we hold on a skeleton instead.
    mocks.viewState.sortField = "name";
    mocks.viewState.filters.availability = ["online"];
    mocks.presence = { byAgent: new Map(), loading: true };

    renderPage();

    expect(screen.queryByText("Alpha Agent")).not.toBeInTheDocument();
    expect(screen.queryByText("Beta Agent")).not.toBeInTheDocument();
    expect(screen.getAllByTestId("skeleton").length).toBeGreaterThan(0);
  });

  it("shows the empty state without blocking on auxiliary queries when there are no agents", () => {
    mocks.agents = [];
    // All auxiliary queries pending — the empty state must not wait on them.
    mocks.activity = { byAgent: new Map(), loading: true };
    mocks.runCountsPending = true;
    mocks.presence = { byAgent: new Map(), loading: true };

    renderPage();

    expect(screen.getByText("No agents yet")).toBeInTheDocument();
    expect(screen.queryByTestId("skeleton")).not.toBeInTheDocument();
  });
});

describe("AgentsPage docs link", () => {
  it("points Learn more at the viewer's docs locale", () => {
    renderPage("fr");

    expect(
      screen.getByRole("link", { name: "En savoir plus →" }),
    ).toHaveAttribute("href", "https://multica.ai/docs/fr/agents");
  });
});

describe("AgentsPage inline runtime/model editing", () => {
  // A single row keeps every trigger query unique across both columns.
  const CATALOG: RuntimeModelsResult = {
    models: [{ id: "claude-fable-5", label: "Fable", provider: "anthropic" }],
    unavailableModels: [],
    supported: true,
  };

  it("shows the model column with labelled triggers for both editable cells", () => {
    mocks.agents = [ALPHA];
    mocks.runtimes = [RT_CLAUDE, RT_CODEX];

    renderPage();

    expect(
      screen.getByRole("button", { name: /^Runtime · Claude/ }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Model · claude" }),
    ).toBeInTheDocument();
  });

  it("rebinds the runtime with model/thinking/tier cleared in one payload", async () => {
    mocks.agents = [ALPHA];
    mocks.runtimes = [RT_CLAUDE, RT_CODEX];

    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /^Runtime · Claude/ }));
    fireEvent.click(await screen.findByRole("button", { name: /^Codex/ }));

    expect(mocks.updateAgentMutate).toHaveBeenCalledTimes(1);
    expect(mocks.updateAgentMutate).toHaveBeenCalledWith(
      {
        id: ALPHA.id,
        data: {
          runtime_id: "runtime-2",
          model: "",
          thinking_level: "",
          service_tier: "",
        },
      },
      expect.anything(),
    );
  });

  it("changes the model from the list", async () => {
    mocks.agents = [ALPHA];
    mocks.runtimes = [RT_CLAUDE];
    mocks.runtimeModels = CATALOG;

    renderPage();
    fireEvent.click(screen.getByRole("button", { name: "Model · claude" }));
    fireEvent.click(await screen.findByText("Fable"));

    expect(mocks.updateAgentMutate).toHaveBeenCalledWith(
      { id: ALPHA.id, data: { model: "claude-fable-5" } },
      expect.anything(),
    );
  });

  it("keeps the cells read-only for rows the user cannot manage", () => {
    // Not the owner, and the empty member list means no admin role.
    mocks.agents = [makeAgent({ id: "a-readonly", owner_id: "user-other" })];
    mocks.runtimes = [RT_CLAUDE];

    renderPage();

    expect(
      screen.queryByRole("button", { name: /^Runtime · / }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^Model · / }),
    ).not.toBeInTheDocument();
    expect(screen.getByText("claude").closest("button")).toBeNull();
    expect(screen.getByText("Claude (mbp.local)").closest("button")).toBeNull();
  });

  it("keeps a cell click from navigating the row", () => {
    mocks.agents = [ALPHA];
    mocks.runtimes = [RT_CLAUDE];
    const adapter = renderPage();

    // Baseline: the row itself navigates.
    fireEvent.click(screen.getByText("Alpha Agent"));
    expect(adapter.push).toHaveBeenCalledWith("/test-workspace/agents/a-alpha");

    // A cell click must not reach the row's handler.
    fireEvent.click(screen.getByRole("button", { name: "Model · claude" }));
    expect(adapter.push).toHaveBeenCalledTimes(1);
  });

  it("omits the model cell when the user hid the column", () => {
    mocks.agents = [ALPHA];
    mocks.viewState.hiddenColumns = ["model", "created"];

    renderPage();

    expect(
      screen.queryByRole("button", { name: /^Model · / }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("claude")).not.toBeInTheDocument();
  });

  it("toasts the shared failure message when an update is rejected", async () => {
    mocks.agents = [ALPHA];
    mocks.runtimes = [RT_CLAUDE];
    mocks.runtimeModels = CATALOG;
    vi.mocked(toast.error).mockClear();

    renderPage();
    fireEvent.click(screen.getByRole("button", { name: "Model · claude" }));
    fireEvent.click(await screen.findByText("Fable"));

    const callbacks = mocks.updateAgentMutate.mock.calls.at(-1)?.[1] as {
      onError: (error: Error) => void;
    };
    callbacks.onError(new Error(""));

    expect(toast.error).toHaveBeenCalledWith("Failed to update agent");
  });
});

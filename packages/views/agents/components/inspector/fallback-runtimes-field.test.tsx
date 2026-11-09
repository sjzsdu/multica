// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import type { AgentFallbackRuntimeEntry } from "@multica/core/types";
import type { RuntimeDevice } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import enIssues from "../../../locales/en/issues.json";

// ModelPicker pulls the runtime model catalog through React Query — not
// under test here, and the query client this unit test doesn't provide.
vi.mock("./model-picker", () => ({
  ModelPicker: () => null,
}));

// Provider logos are inline SVGs with no behavior under test.
vi.mock("../../../runtimes/components/provider-logo", () => ({
  ProviderLogo: () => null,
}));

import { FallbackRuntimesField } from "./fallback-runtimes-field";

const TEST_RESOURCES = {
  en: { common: enCommon, agents: enAgents, issues: enIssues },
};

const ME = "user-me";

function makeRuntime(overrides: Partial<RuntimeDevice>): RuntimeDevice {
  return {
    id: "rt",
    workspace_id: "ws-1",
    daemon_id: null,
    name: "Claude (host.local)",
    runtime_mode: "local",
    provider: "claude",
    launch_header: "",
    status: "online",
    device_info: "host.local · macOS (arm64)",
    metadata: {},
    owner_id: ME,
    visibility: "private",
    last_seen_at: "2026-07-11T00:00:00Z",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

const PRIMARY_ID = "rt-primary";
const RT_A = makeRuntime({ id: "rt-a", name: "Codex (host.local)", provider: "codex" });
const RT_B = makeRuntime({ id: "rt-b", name: "Gemini (host.local)", provider: "gemini" });
const RUNTIMES = [makeRuntime({ id: PRIMARY_ID }), RT_A, RT_B];

const ENTRY_A: AgentFallbackRuntimeEntry = { runtime_id: "rt-a", model: "gpt-6-sol" };

function renderField(
  props: Partial<React.ComponentProps<typeof FallbackRuntimesField>> = {},
) {
  const onChange = vi.fn();
  const utils = render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <FallbackRuntimesField
        value={[ENTRY_A]}
        primaryRuntimeId={PRIMARY_ID}
        runtimes={RUNTIMES}
        currentUserId={ME}
        canEdit
        onChange={onChange}
        {...props}
      />
    </I18nProvider>,
  );
  return { ...utils, onChange };
}

function openAddPicker() {
  fireEvent.click(screen.getByRole("button", { name: "Add fallback runtime" }));
}

describe("FallbackRuntimesField", () => {
  beforeEach(() => cleanup());
  afterEach(() => cleanup());

  it("renders one ordered row per chain entry", () => {
    renderField();
    expect(screen.getByText("Codex")).toBeTruthy();
    expect(screen.getByText("1")).toBeTruthy();
  });

  it("appends a new entry via the add picker and normalizes it", () => {
    const { onChange } = renderField({ value: [] });
    openAddPicker();
    fireEvent.click(screen.getByRole("button", { name: /Gemini/ }));
    expect(onChange).toHaveBeenCalledWith([{ runtime_id: "rt-b" }]);
  });

  it("offers only runtimes that pass the server validator", () => {
    renderField();
    openAddPicker();
    // Primary is never offered; already-chained rt-a is not offered again.
    expect(screen.queryByRole("button", { name: /^Claude/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /^Codex/ })).toBeNull();
    expect(screen.getByRole("button", { name: /Gemini/ })).toBeTruthy();
  });

  it("removes an entry", () => {
    const { onChange } = renderField();
    fireEvent.click(screen.getByRole("button", { name: "Remove fallback runtime 1" }));
    expect(onChange).toHaveBeenCalledWith([]);
  });

  it("reorders entries with the move controls", () => {
    const { onChange } = renderField({
      value: [ENTRY_A, { runtime_id: "rt-b" }],
    });
    fireEvent.click(screen.getByRole("button", { name: "Move fallback runtime 2 up" }));
    expect(onChange).toHaveBeenCalledWith([
      { runtime_id: "rt-b" },
      { runtime_id: "rt-a", model: "gpt-6-sol" },
    ]);
  });

  it("keeps optional overrides when rewriting the chain", () => {
    const { onChange } = renderField({
      value: [
        {
          runtime_id: "rt-a",
          thinking_level: "high",
          service_tier: "priority",
        },
        { runtime_id: "rt-b" },
      ],
    });
    fireEvent.click(screen.getByRole("button", { name: "Move fallback runtime 2 up" }));
    expect(onChange).toHaveBeenCalledWith([
      { runtime_id: "rt-b" },
      {
        runtime_id: "rt-a",
        thinking_level: "high",
        service_tier: "priority",
      },
    ]);
  });

  it("hides editing controls in read-only mode", () => {
    renderField({ canEdit: false });
    expect(
      screen.queryByRole("button", { name: "Add fallback runtime" }),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Remove fallback runtime 1" }),
    ).toBeNull();
    expect(screen.getByText("Codex")).toBeTruthy();
  });

  it("shows the empty hint when the chain is empty", () => {
    renderField({ value: [] });
    expect(
      screen.getByText(
        "No fallback runtimes. The agent only runs on its primary runtime.",
      ),
    ).toBeTruthy();
  });
});

import { beforeEach, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type { StorageMigrationState } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { StorageMigrationSection } from "./storage-migration-section";

vi.mock("@multica/core/api", () => ({
  api: { getStorageMigrationStatus: vi.fn(), startStorageMigration: vi.fn() },
}));

const getStatus = vi.mocked(api.getStorageMigrationStatus);
const start = vi.mocked(api.startStorageMigration);

const idleState: StorageMigrationState = {
  running: false,
  total: 0,
  migrated: 0,
  failed: 0,
  local_cleaned: 0,
  failures: [],
  dropped_failures: 0,
};

function mount() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <StorageMigrationSection wsId="ws-1" />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  getStatus.mockReset();
  start.mockReset();
});

it("stays hidden when the server has no object store configured", async () => {
  getStatus.mockResolvedValue({ available: false });
  mount();
  await waitFor(() => expect(getStatus).toHaveBeenCalled());
  expect(screen.queryByText("Local file migration")).toBeNull();
});

it("shows the pending tally and starts a migration", async () => {
  getStatus.mockResolvedValue({
    available: true,
    pending: { pending_attachments: 3, pending_avatars: 1 },
    state: idleState,
  });
  start.mockResolvedValue({ started: true });
  mount();

  expect(
    await screen.findByText(
      "3 attachments and 1 avatars still live on this server's local disk.",
    ),
  ).toBeVisible();
  await userEvent.click(screen.getByRole("button", { name: "Migrate files" }));
  await waitFor(() => expect(start).toHaveBeenCalledTimes(1));
});

it("reports live progress while a run is in flight", async () => {
  getStatus
    .mockResolvedValueOnce({
      available: true,
      pending: { pending_attachments: 2, pending_avatars: 0 },
      state: idleState,
    })
    .mockResolvedValue({
      available: true,
      pending: { pending_attachments: 1, pending_avatars: 0 },
      state: { ...idleState, running: true, migrated: 1 },
    });
  start.mockResolvedValue({ started: true });
  mount();

  await userEvent.click(
    await screen.findByRole("button", { name: "Migrate files" }),
  );
  expect(
    await screen.findByText(
      "Moving files to object storage… 1 moved, 0 failed.",
    ),
  ).toBeVisible();
  // No second start is offered mid-run; the server would answer 409 anyway.
  expect(screen.queryByRole("button", { name: "Migrate files" })).toBeNull();
});

it("summarizes a finished run with its failure count", async () => {
  getStatus.mockResolvedValue({
    available: true,
    pending: { pending_attachments: 0, pending_avatars: 0 },
    state: {
      ...idleState,
      migrated: 4,
      failed: 1,
      finished_at: "2026-10-18T00:00:00Z",
    },
  });
  mount();

  expect(
    await screen.findByText("Moved 4 files to object storage, 1 failed."),
  ).toBeVisible();
});

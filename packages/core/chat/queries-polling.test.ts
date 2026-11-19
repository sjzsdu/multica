// @vitest-environment jsdom

import { QueryClient, QueryObserver } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";

import { pendingChatTaskOptions } from "./queries";

describe("pendingChatTaskOptions polling recovery", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("drives a pending task to terminal state and stops polling", async () => {
    vi.useFakeTimers();
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const queryFn = vi.fn()
      .mockResolvedValueOnce({ task_id: "task-1", status: "dispatched" })
      .mockResolvedValueOnce({});
    const observer = new QueryObserver(queryClient, {
      ...pendingChatTaskOptions("session-1"),
      queryFn,
    });
    const unsubscribe = observer.subscribe(() => undefined);

    await observer.refetch();
    expect(queryFn).toHaveBeenCalledTimes(1);
    expect(observer.getCurrentResult().data).toEqual({
      task_id: "task-1",
      status: "dispatched",
    });

    await vi.advanceTimersByTimeAsync(5_000);
    await vi.waitFor(() => expect(observer.getCurrentResult().data).toEqual({}));
    expect(queryFn).toHaveBeenCalledTimes(2);

    await vi.advanceTimersByTimeAsync(15_000);
    expect(queryFn).toHaveBeenCalledTimes(2);

    unsubscribe();
    queryClient.clear();
  });
});

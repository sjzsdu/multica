import { useEffect, useState } from "react";
import { AgentsPage } from "@multica/views/agents";
import { useAgentsViewStore } from "@multica/core/agents/stores";
import type { DaemonStatus } from "../../../shared/daemon-types";

/**
 * Desktop wrapper around the shared `AgentsPage`. Bridges the Electron
 * `daemonAPI` (main-process daemon state) into the page so the runtime
 * machine filter can render the Local section the same way the Runtimes
 * page does — without these props the page falls back to grouping
 * every local-mode runtime under "Remote" with a generic title, which
 * breaks the "drill from a machine into its agents" promise of the
 * filter.
 *
 * Mirrors `DesktopRuntimesPage`: we cache the last seen daemon
 * identity so the Local row doesn't get reclassified as Remote when
 * the daemon is stopped (which would null out `status.daemonId`), and
 * we fall back to the OS hostname so the section label stays useful
 * even when the app doesn't manage the running daemon (WSL2 etc.).
 *
 * Desktop-specific: auto-enable runtime + model columns on mount.
 * Web and Desktop persist their hidden-column choice in separate
 * localStorage backends, so a Desktop user who toggled those columns
 * off in an older build would keep them hidden even after upgrade —
 * this ensures Desktop stays visually aligned with the Web app where
 * runtime/model are always visible by default.
 */
export function DesktopAgentsPage() {
  const [status, setStatus] = useState<DaemonStatus>({ state: "stopped" });
  const [lastIdentity, setLastIdentity] = useState<{
    daemonId: string | null;
    deviceName: string | null;
  }>({ daemonId: null, deviceName: null });
  const [hostName, setHostName] = useState<string | null>(null);

  // Force runtime + model columns visible on Desktop mount.
  // Zustand persist reads from Electron localStorage which is isolated
  // from browser localStorage, so older Desktop builds can leave these
  // columns hidden even after the Web version defaulted them on.
  const toggleColumn = useAgentsViewStore((s) => s.toggleColumn);
  const hiddenColumns = useAgentsViewStore((s) => s.hiddenColumns);

  useEffect(() => {
    const apply = (s: DaemonStatus) => {
      setStatus(s);
      if (s.daemonId) {
        setLastIdentity({
          daemonId: s.daemonId,
          deviceName: s.deviceName ?? null,
        });
      }
    };
    window.daemonAPI.getStatus().then(apply);
    window.daemonAPI.getHostName().then((name) => setHostName(name || null));
    return window.daemonAPI.onStatusChange(apply);
  }, []);

  // One-shot on mount: lift runtime + model columns out of hidden state
  // if the Desktop user has persisted them off in localStorage (which is
  // Electron-isolated from the browser's store). We run this exactly once
  // per mount so a user who later toggles a column off on purpose isn't
  // silently re-enabled.
  useEffect(() => {
    if (hiddenColumns.includes("runtime")) toggleColumn("runtime");
    if (hiddenColumns.includes("model")) toggleColumn("model");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <AgentsPage
      localDaemonId={status.daemonId ?? lastIdentity.daemonId}
      localMachineName={status.deviceName ?? lastIdentity.deviceName ?? hostName}
      // Desktop owns a local machine for the lifetime of the app, even
      // while the daemon is stopped or hasn't registered yet. The shared
      // page synthesizes a placeholder local row so the filter dropdown
      // still has a Local option to pick in the empty window.
      hasLocalMachine
    />
  );
}

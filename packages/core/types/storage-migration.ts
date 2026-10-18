/**
 * Local→object-store storage migration (self-hosted cutover). One run is
 * instance-wide; the endpoints are gated to workspace owner/admin and the
 * status is only reported when the server has an object store configured.
 */

export interface StorageMigrationFailure {
  kind: string;
  id: string;
  url: string;
  error: string;
}

export interface StorageMigrationState {
  running: boolean;
  total: number;
  migrated: number;
  failed: number;
  local_cleaned: number;
  /** Bounded to the first N failures server-side; dropped_failures counts the rest. */
  failures: StorageMigrationFailure[];
  dropped_failures: number;
  started_at?: string | null;
  finished_at?: string | null;
}

export interface StorageMigrationPending {
  pending_attachments: number;
  pending_avatars: number;
}

export interface StorageMigrationStatus {
  /** False when the server has no object store configured — hide the UI. */
  available: boolean;
  pending?: StorageMigrationPending;
  state?: StorageMigrationState;
}

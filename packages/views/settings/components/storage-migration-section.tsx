"use client";

import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { Button } from "@multica/ui/components/ui/button";
import { storageMigrationOptions } from "@multica/core/workspace/queries";
import { useStartStorageMigration } from "@multica/core/workspace/mutations";
import { useT } from "../../i18n";
import { SettingsCard, SettingsRow, SettingsSection } from "./settings-layout";

/**
 * Self-hosted local→object-store storage migration, surfaced in workspace
 * settings for owners/admins. Renders nothing unless the server reports the
 * feature available (an object store is configured) AND there is something
 * worth saying: files still on local disk, a run in flight, or a finished
 * run from this session.
 */
export function StorageMigrationSection({ wsId }: { wsId: string }) {
  const { t } = useT("settings");
  const { data: status } = useQuery(storageMigrationOptions(wsId));
  const startMigration = useStartStorageMigration(wsId);

  if (!status?.available) return null;

  const pending = status.pending;
  const state = status.state;
  const pendingTotal =
    (pending?.pending_attachments ?? 0) + (pending?.pending_avatars ?? 0);
  const running = state?.running ?? false;
  const hasFinishedRun =
    !!state?.finished_at && (state.migrated > 0 || state.failed > 0);
  if (!running && pendingTotal === 0 && !hasFinishedRun) return null;

  const attachmentsNoun =
    (pending?.pending_attachments ?? 0) === 1 ? "attachment" : "attachments";
  const avatarsNoun =
    (pending?.pending_avatars ?? 0) === 1 ? "avatar" : "avatars";
  const filesNoun =
    (state?.migrated ?? 0) === 1 ? "file" : "files";

  const description = running
    ? t(($) => $.workspace.storage_migration_running, {
        migrated: state?.migrated ?? 0,
        failed: state?.failed ?? 0,
        files_noun: filesNoun,
      })
    : hasFinishedRun
      ? t(($) => $.workspace.storage_migration_finished, {
          migrated: state?.migrated ?? 0,
          failed: state?.failed ?? 0,
          files_noun: filesNoun,
        })
      : t(($) => $.workspace.storage_migration_pending, {
          attachments: pending?.pending_attachments ?? 0,
          avatars: pending?.pending_avatars ?? 0,
          attachments_noun: attachmentsNoun,
          avatars_noun: avatarsNoun,
        });

  return (
    <SettingsSection title={t(($) => $.workspace.storage_section)}>
      <SettingsCard>
        <SettingsRow
          anchor="storage-migration"
          label={t(($) => $.workspace.storage_migration_label)}
          description={description}
        >
          {running ? null : (
            <Button
              variant="outline"
              size="sm"
              disabled={startMigration.isPending}
              onClick={() =>
                startMigration.mutate(undefined, {
                  onError: (error) =>
                    toast.error(
                      error instanceof Error
                        ? error.message
                        : t(($) => $.workspace.storage_migration_start_failed),
                    ),
                })
              }
            >
              {t(($) => $.workspace.storage_migration_start)}
            </Button>
          )}
        </SettingsRow>
      </SettingsCard>
    </SettingsSection>
  );
}
// applyGitHubSSHHostAlias converts the three GitHub URL forms accepted by the
// API to an SSH-config Host alias. Keeping this transformation in a small pure
// helper makes the UI explicit about account selection without introducing a
// second credential store: ssh still resolves the alias on the daemon machine.
export function applyGitHubSSHHostAlias(
  repositoryURL: string,
  sshHostAlias: string,
): { url: string; applied: boolean } {
  const url = repositoryURL.trim();
  const alias = sshHostAlias.trim();
  if (!alias) return { url, applied: false };
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(alias)) {
    return { url, applied: false };
  }

  const scp = url.match(/^([^@:\s]+@)?github\.com:([^\s]+)$/i);
  if (scp) {
    return { url: `${scp[1] ?? "git@"}${alias}:${scp[2]}`, applied: true };
  }

  const ssh = url.match(/^ssh:\/\/([^@/\s]+@)?github\.com(?::\d+)?\/([^\s]+)$/i);
  if (ssh) {
    return { url: `ssh://${ssh[1] ?? "git@"}${alias}/${ssh[2]}`, applied: true };
  }

  const https = url.match(/^https?:\/\/github\.com\/([^\s]+)$/i);
  if (https) {
    return { url: `git@${alias}:${https[1]}`, applied: true };
  }

  return { url, applied: false };
}

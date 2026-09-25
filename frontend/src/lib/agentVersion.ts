/**
 * Node Agent capability gates in the UI.
 *
 * Compares the numeric MAJOR.MINOR.PATCH core of the version reported by the
 * node's last heartbeat against a minimum, number by number (never as
 * strings, so 2.0.0 >= 1.1.3 and 1.10.0 > 1.9.0). Pre-release suffixes
 * (1.1.0-rc11) and build metadata are compared by their numeric core, like the
 * rc builds that already shipped the features.
 *
 * Unknown or unparsable versions return true: the Panel API is authoritative
 * and rejects unsupported routes with a stable 422 code, so the UI only hints.
 */
export function agentVersionAtLeast(agentVersion: string | undefined | null, minimum: string): boolean {
  const match = /^v?(\d+)\.(\d+)\.(\d+)/.exec((agentVersion ?? '').trim());
  if (!match) return true;
  const have = match.slice(1).map(Number);
  const need = minimum.split('.').map(Number);
  for (let i = 0; i < 3; i += 1) {
    if (have[i] !== need[i]) return have[i] > need[i];
  }
  return true;
}

/**
 * Release version accepted by POST /api/v1/agent-releases (internal/panel/releases.go):
 * starts with a letter or digit, then up to 63 of letters, digits, `.`, `_`, `+`, `-`.
 */
export const RELEASE_VERSION_PATTERN = /^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$/;

export function isValidReleaseVersion(version: string): boolean {
  return RELEASE_VERSION_PATTERN.test(version.trim());
}

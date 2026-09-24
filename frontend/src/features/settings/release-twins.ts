import type { AgentRelease } from '../../lib/contracts';

export interface ReleaseTwin {
  /** 'same' — identical SHA-256 (re-upload); 'rebuilt' — same version, different binary. */
  kind: 'same' | 'rebuilt';
  /** Sequence of the newer release that supersedes this one. */
  sequence: number;
}

/**
 * Marks the OLDER of two releases that look alike (same platform and either the
 * same binary or the same version string). The newest one stays unmarked, so
 * the list reads «#20 — тот же бинарник, что #21». Purely presentational: the
 * backend still treats every sequence as a distinct release.
 */
export function releaseTwinMap(releases: AgentRelease[]): Map<string, ReleaseTwin> {
  const result = new Map<string, ReleaseTwin>();
  const newestFirst = [...releases].sort((a, b) => b.sequence - a.sequence);
  const platform = (release: AgentRelease) => `${release.os.trim().toLowerCase()}/${release.arch.trim().toLowerCase()}`;
  newestFirst.forEach((release, index) => {
    // Closest newer release first.
    const newer = newestFirst.slice(0, index).filter((candidate) => platform(candidate) === platform(release)).reverse();
    const sameBinary = newer.find((candidate) => candidate.sha256 === release.sha256);
    if (sameBinary) { result.set(release.id, { kind: 'same', sequence: sameBinary.sequence }); return; }
    const sameVersion = newer.find((candidate) => candidate.version === release.version);
    if (sameVersion) result.set(release.id, { kind: 'rebuilt', sequence: sameVersion.sequence });
  });
  return result;
}

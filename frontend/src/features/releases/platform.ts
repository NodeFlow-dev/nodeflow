import type { AgentRelease, HeartbeatMetrics } from '../../lib/contracts';

export interface AgentPlatform {
  os: string;
  arch: string;
}

export function heartbeatPlatform(metrics?: HeartbeatMetrics): AgentPlatform | null {
  const os = typeof metrics?.os === 'string' ? metrics.os.trim().toLowerCase() : '';
  const arch = typeof metrics?.arch === 'string' ? metrics.arch.trim().toLowerCase() : '';
  return os && arch ? { os, arch } : null;
}

export function releaseMatchesPlatform(release: AgentRelease, platform: AgentPlatform | null): boolean {
  return Boolean(platform)
    && release.os.trim().toLowerCase() === platform?.os
    && release.arch.trim().toLowerCase() === platform?.arch;
}

export function platformLabel(platform: AgentPlatform | null): string {
  return platform ? `${platform.os} / ${platform.arch}` : 'Платформа неизвестна';
}

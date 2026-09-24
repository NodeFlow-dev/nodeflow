/**
 * Shared options for live dashboard queries.
 *
 * Polling stops entirely while the document is hidden: the interval function
 * returns `false`, which clears TanStack's timer instead of waking every 15 s.
 * When the tab becomes visible again, `refetchOnWindowFocus: 'always'` fires
 * one refetch immediately; its completion re-arms the 15 s interval.
 */
export const LIVE_POLL_MS = 15_000;

export function isDocumentHidden(): boolean {
  return typeof document !== 'undefined' && document.visibilityState === 'hidden';
}

export const livePollingOptions = {
  refetchInterval: () => (isDocumentHidden() ? false : LIVE_POLL_MS),
  refetchIntervalInBackground: false,
  refetchOnWindowFocus: 'always',
  staleTime: LIVE_POLL_MS / 2,
} as const;

/** Agent releases change only on explicit upload/delete; never poll them. */
export const agentReleasesQueryKey = ['agent-releases'] as const;

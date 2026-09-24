import { Link, useLocation } from 'react-router-dom';
import { useEffect, useState } from 'react';
import { demoSuffix } from '../lib/navigation';

let cachedPanelVersion = '';
let panelVersionRequest: Promise<string> | null = null;

function loadPanelVersion() {
  if (cachedPanelVersion) return Promise.resolve(cachedPanelVersion);
  panelVersionRequest ??= fetch('/healthz', { credentials: 'same-origin' })
    .then(async (response) => {
      if (!response.ok) return '';
      const payload = await response.json() as { version?: unknown };
      return typeof payload.version === 'string' ? payload.version.trim() : '';
    })
    .catch(() => '')
    .then((version) => {
      if (version) cachedPanelVersion = version;
      return version;
    })
    .finally(() => {
      if (!cachedPanelVersion) panelVersionRequest = null;
    });
  return panelVersionRequest;
}

export function NodeFlowLogo() {
  const location = useLocation();
  const [panelVersion, setPanelVersion] = useState(cachedPanelVersion);

  useEffect(() => {
    let active = true;
    let retryTimer: number | undefined;
    const refresh = () => {
      void loadPanelVersion().then((version) => {
        if (!active) return;
        if (version) {
          setPanelVersion(version);
          return;
        }
        retryTimer = window.setTimeout(refresh, 15_000);
      });
    };
    refresh();
    return () => {
      active = false;
      window.clearTimeout(retryTimer);
    };
  }, []);

  return (
    <Link to={`/nodes${demoSuffix(location.search)}`} className="nf-logo" aria-label="NodeFlow — ноды">
      <svg className="nf-logo__mark" viewBox="0 0 38 28" aria-hidden="true">
        <path d="M2 25V3h5.4l9.1 12.4V3H22v22h-5.3L7.5 12.5V25H2Z" />
        <path d="M20 3h16v5H26v4h8.5v5H26v8h-6V3Z" />
      </svg>
      <span className="nf-logo__identity">
        <span className="nf-logo__name">NodeFlow</span>
        {panelVersion && (
          <span className="nf-logo__version" aria-label={`Версия панели ${panelVersion}`}>
            v{panelVersion}
          </span>
        )}
      </span>
    </Link>
  );
}

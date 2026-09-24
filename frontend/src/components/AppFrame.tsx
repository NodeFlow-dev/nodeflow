import { ActionIcon, AppShell, Burger, Stack, Text } from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { IconChartLine, IconLogout, IconSettings, IconStack2, IconUserCircle } from '@tabler/icons-react';
import { useEffect, useRef, useState } from 'react';
import { NavLink, Outlet, useLocation } from 'react-router-dom';
import { api } from '../lib/api';
import { preloadAppRoute, preloadInternalLink } from '../lib/navigation';
import { LoginPanel } from './LoginPanel';
import { NodeFlowLogo } from './NodeFlowLogo';

const navigation = [
  { to: '/nodes', label: 'Ноды', icon: IconStack2 },
  { to: '/traffic', label: 'Трафик', icon: IconChartLine },
  { to: '/settings', label: 'Настройки', icon: IconSettings },
];

export function AppFrame() {
  const [opened, { toggle, close }] = useDisclosure();
  const activitySentAt = useRef(0);
  const activityPending = useRef(false);
  const location = useLocation();
  const demoQuery = new URLSearchParams(location.search).get('demo') === '1' ? '?demo=1' : '';
  const [authState, setAuthState] = useState<'checking' | 'authenticated' | 'unauthenticated'>(
    demoQuery ? 'authenticated' : 'checking',
  );

  useEffect(() => {
    const expire = () => setAuthState('unauthenticated');
    window.addEventListener('nodeflow:unauthorized', expire);
    return () => window.removeEventListener('nodeflow:unauthorized', expire);
  }, []);

  useEffect(() => {
    if (demoQuery) {
      setAuthState('authenticated');
      return undefined;
    }
    let active = true;
    setAuthState('checking');
    void api('/auth/session')
      .then(() => {
        if (active) setAuthState('authenticated');
      })
      .catch(() => {
        if (active) setAuthState('unauthenticated');
      });
    return () => { active = false; };
  }, [demoQuery]);

  useEffect(() => {
    if (demoQuery) return undefined;
    const recordActivity = () => {
      const now = Date.now();
      if (authState !== 'authenticated' || activityPending.current || now - activitySentAt.current < 30_000) return;
      activitySentAt.current = now;
      activityPending.current = true;
      void api('/auth/activity', { method: 'POST' }).catch(() => undefined).finally(() => {
        activityPending.current = false;
      });
    };
    window.addEventListener('pointerdown', recordActivity, true);
    window.addEventListener('keydown', recordActivity, true);
    window.addEventListener('wheel', recordActivity, { capture: true, passive: true });
    return () => {
      window.removeEventListener('pointerdown', recordActivity, true);
      window.removeEventListener('keydown', recordActivity, true);
      window.removeEventListener('wheel', recordActivity, true);
    };
  }, [authState, demoQuery]);

  const logout = async () => {
    await api('/auth/logout', { method: 'POST' }).catch(() => undefined);
    setAuthState('unauthenticated');
  };

  if (authState === 'checking') {
    return <div className="nf-auth-loading" aria-label="Проверка сессии" />;
  }

  if (authState === 'unauthenticated') {
    return (
      <LoginPanel
        onSuccess={() => {
          activitySentAt.current = Date.now();
          setAuthState('authenticated');
        }}
      />
    );
  }

  return (
    <AppShell
      className="nf-shell"
      header={{ height: 60, collapsed: false, offset: false }}
      navbar={{ width: 202, breakpoint: 'md', collapsed: { mobile: !opened } }}
      padding={0}
    >
      <AppShell.Header className="nf-mobile-header" hiddenFrom="md">
        <NodeFlowLogo />
        <Burger opened={opened} onClick={toggle} size="sm" aria-label={opened ? 'Закрыть меню' : 'Открыть меню'} />
      </AppShell.Header>

      <AppShell.Navbar className="nf-sidebar" aria-label="Основная навигация">
        <div className="nf-sidebar__brand"><NodeFlowLogo /></div>
        <Stack gap={8} className="nf-sidebar__nav">
          {navigation.map(({ to, label, icon: Icon }) => {
            const active = location.pathname === to || (to === '/nodes' && location.pathname.startsWith('/nodes/'));
            return (
              <NavLink
                key={to}
                to={`${to}${demoQuery}`}
                onPointerEnter={() => preloadAppRoute(to)}
                onFocus={() => preloadAppRoute(to)}
                onClick={close}
                className={`nf-nav-item${active ? ' is-active' : ''}`}
              >
                <Icon size={20} stroke={1.7} aria-hidden="true" />
                <span>{label}</span>
              </NavLink>
            );
          })}
        </Stack>
        <div className="nf-sidebar__account">
          <IconUserCircle size={32} stroke={1.4} aria-hidden="true" />
          <div>
            <Text size="sm" fw={500}>Оператор</Text>
            <Text size="xs" c="dimmed">Локальный оператор</Text>
          </div>
          <ActionIcon variant="subtle" color="gray" onClick={logout} aria-label="Выйти">
            <IconLogout size={18} stroke={1.6} />
          </ActionIcon>
        </div>
      </AppShell.Navbar>

      <AppShell.Main
        className="nf-workspace"
        onPointerOver={(event) => preloadInternalLink(event.target)}
        onPointerDownCapture={(event) => preloadInternalLink(event.target)}
        onFocusCapture={(event) => preloadInternalLink(event.target)}
      >
        <Outlet />
      </AppShell.Main>
    </AppShell>
  );
}

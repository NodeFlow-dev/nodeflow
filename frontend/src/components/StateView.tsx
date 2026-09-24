import { Button } from '@mantine/core';
import { IconAlertTriangle, IconDatabaseOff } from '@tabler/icons-react';
import type { ReactNode } from 'react';

interface StateViewProps {
  title: string;
  description: string;
  action?: ReactNode;
  tone?: 'empty' | 'error';
}

export function StateView({ title, description, action, tone = 'empty' }: StateViewProps) {
  const Icon = tone === 'error' ? IconAlertTriangle : IconDatabaseOff;
  return (
    <div className={`nf-state-view nf-state-view--${tone}`} role={tone === 'error' ? 'alert' : 'status'}>
      <Icon size={28} stroke={1.5} aria-hidden="true" />
      <div><strong>{title}</strong><span>{description}</span></div>
      {action}
    </div>
  );
}

export function RetryButton({ onClick }: { onClick: () => void }) {
  return <Button variant="light" color="nodeflow" onClick={onClick}>Повторить</Button>;
}

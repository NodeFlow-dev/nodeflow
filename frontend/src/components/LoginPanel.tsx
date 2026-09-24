import { Alert, Button, PasswordInput } from '@mantine/core';
import { useState, type FormEvent } from 'react';
import { IconAlertCircle, IconLock } from '@tabler/icons-react';
import { api } from '../lib/api';
import { NodeFlowLogo } from './NodeFlowLogo';

export function LoginPanel({ onSuccess }: { onSuccess: () => void }) {
  const [token, setToken] = useState('');
  const [error, setError] = useState('');
  const [pending, setPending] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setPending(true); setError('');
    try {
      await api('/auth/login', { method: 'POST', body: JSON.stringify({ token: token.trim() }) });
      setToken(''); onSuccess();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Не удалось войти');
    } finally { setPending(false); }
  };
  return (
    <div className="nf-login">
      <form className="nf-login__panel" onSubmit={submit}>
        <NodeFlowLogo />
        <div><h1>Вход в панель</h1><p>Используйте административный токен. После входа он заменяется защищённой browser-сессией.</p></div>
        {error && <Alert color="red" icon={<IconAlertCircle size={18} />}>{error}</Alert>}
        <PasswordInput label="Токен панели" value={token} onChange={(event) => setToken(event.currentTarget.value)} leftSection={<IconLock size={16} />} required autoFocus />
        <Button type="submit" loading={pending} disabled={!token.trim()}>Войти</Button>
      </form>
    </div>
  );
}

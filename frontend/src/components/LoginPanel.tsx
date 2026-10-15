import { Alert, Button, PasswordInput } from '@mantine/core';
import { useState, type FormEvent } from 'react';
import { IconAlertCircle, IconLock } from '@tabler/icons-react';
import { api } from '../lib/api';
import { LanguageSwitch } from './LanguageMenu';
import { NodeFlowLogo } from './NodeFlowLogo';
import { t } from '../i18n';

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
      setError(reason instanceof Error ? reason.message : t('app.signInFailed'));
    } finally { setPending(false); }
  };
  return (
    <div className="nf-login">
      <form className="nf-login__panel" onSubmit={submit}>
        <div className="nf-login__top"><NodeFlowLogo /><LanguageSwitch /></div>
        <div><h1>{t('app.signInToThePanel')}</h1><p>{t('app.useTheAdminTokenAfter')}</p></div>
        {error && <Alert color="red" icon={<IconAlertCircle size={18} />}>{error}</Alert>}
        <PasswordInput label={t('app.panelToken')} value={token} onChange={(event) => setToken(event.currentTarget.value)} leftSection={<IconLock size={16} />} required autoFocus />
        <Button type="submit" loading={pending} disabled={!token.trim()}>{t('app.signIn')}</Button>
      </form>
    </div>
  );
}

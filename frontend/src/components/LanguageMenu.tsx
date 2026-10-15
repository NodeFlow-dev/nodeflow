import { Menu, SegmentedControl } from '@mantine/core';
import { IconLanguage } from '@tabler/icons-react';
import { getLang, LANGS, setLang, t, type Lang } from '../i18n';

const labels: Record<Lang, string> = { ru: t('app.languageRu'), en: t('app.languageEn') };

/** Sidebar language row above the account block, styled like a nav item. */
export function LanguageMenu() {
  const current = getLang();
  return (
    <Menu position="right-end" withinPortal>
      <Menu.Target>
        <button type="button" className="nf-nav-item nf-sidebar__lang" aria-label={`${t('app.language')}: ${labels[current]}`}>
          <IconLanguage size={20} stroke={1.7} aria-hidden="true" />
          <span>{labels[current]}</span>
          <small>{current.toUpperCase()}</small>
        </button>
      </Menu.Target>
      <Menu.Dropdown>
        <Menu.Label>{t('app.language')}</Menu.Label>
        {LANGS.map((lang) => (
          <Menu.Item key={lang} lang={lang} onClick={() => setLang(lang)} rightSection={lang === current ? '✓' : null}>
            {labels[lang]}
          </Menu.Item>
        ))}
      </Menu.Dropdown>
    </Menu>
  );
}

/** Compact RU / EN switch for screens without the sidebar (sign-in). */
export function LanguageSwitch() {
  return (
    <SegmentedControl size="xs" value={getLang()} onChange={(value) => setLang(value as Lang)} aria-label={t('app.language')}
      data={LANGS.map((lang) => ({ value: lang, label: lang.toUpperCase() }))} />
  );
}

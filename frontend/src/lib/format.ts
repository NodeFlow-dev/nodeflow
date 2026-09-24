export const formatNumber = (value: number | null | undefined, maximumFractionDigits = 0): string =>
  Number.isFinite(value) ? new Intl.NumberFormat('ru-RU', { maximumFractionDigits }).format(Number(value)) : '—';

export function formatBytes(value: number | null | undefined): string {
  if (!Number.isFinite(value)) return '—';
  const units = ['Б', 'КиБ', 'МиБ', 'ГиБ', 'ТиБ', 'ПиБ'];
  let result = Math.max(0, Number(value));
  let index = 0;
  while (result >= 1024 && index < units.length - 1) {
    result /= 1024;
    index += 1;
  }
  return `${formatNumber(result, result >= 100 ? 0 : result >= 10 ? 1 : 2)} ${units[index]}`;
}

export function formatBitrate(value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return '—';
  const units = ['бит/с', 'Кбит/с', 'Мбит/с', 'Гбит/с', 'Тбит/с'];
  let result = Math.max(0, Number(value));
  let index = 0;
  while (result >= 1000 && index < units.length - 1) {
    result /= 1000;
    index += 1;
  }
  return `${formatNumber(result, result >= 100 || index === 0 ? 0 : result >= 10 ? 1 : 2)} ${units[index]}`;
}

export function formatMonth(value?: string): string {
  if (!/^\d{4}-\d{2}$/.test(value ?? '')) return 'текущий месяц';
  const [year, month] = value!.split('-').map(Number);
  return new Intl.DateTimeFormat('ru-RU', { month: 'long', year: 'numeric', timeZone: 'UTC' }).format(
    new Date(Date.UTC(year, month - 1, 1)),
  );
}

export function shortHAProxy(value?: string): string {
  const match = String(value ?? '').match(/(?:HAProxy\s+version\s+)?(\d+\.\d+\.\d+)(?:[-+\s]|$)/i);
  return match?.[1] ?? value ?? '—';
}

export function timeAgo(value?: string): string {
  if (!value) return 'никогда';
  const seconds = Math.max(0, (Date.now() - Date.parse(value)) / 1000);
  if (seconds < 60) return `${Math.floor(seconds)} сек назад`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)} мин назад`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} ч назад`;
  return `${Math.floor(seconds / 86400)} д назад`;
}

export function formatDuration(value: number | null | undefined): string {
  if (!Number.isFinite(value)) return '—';
  let seconds = Math.max(0, Math.floor(Number(value)));
  const days = Math.floor(seconds / 86_400);
  seconds %= 86_400;
  const hours = Math.floor(seconds / 3_600);
  seconds %= 3_600;
  const minutes = Math.floor(seconds / 60);
  if (days) return `${days} д. ${hours} ч. ${minutes} м.`;
  if (hours) return `${hours} ч. ${minutes} м.`;
  return `${minutes} мин.`;
}

export function formatDateTime(value?: string): string {
  if (!value || !Number.isFinite(Date.parse(value))) return '—';
  return new Intl.DateTimeFormat('ru-RU', {
    day: '2-digit', month: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit',
  }).format(new Date(value));
}

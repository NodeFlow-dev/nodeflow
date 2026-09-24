import type { PanelSettings, PanelTheme } from './contracts';

export const defaultAccent = '#C27087';
export const accentPattern = /^#[0-9A-Fa-f]{6}$/;
export const themeAccents: Partial<Record<PanelTheme, string>> = {
  green: '#22C55E',
  rose: '#C27087',
  cyan: '#45C7D8',
  amber: '#E7B84B',
};

const accentStorageKey = 'nodeflow.appearance.accent';
const themeStorageKey = 'nodeflow.appearance.theme';
let activeTheme: PanelTheme = 'rose';
let activeAccent = defaultAccent;
let systemThemeListenerInstalled = false;

type RGB = readonly [number, number, number];

function readStorage(key: string) {
  try { return localStorage.getItem(key); } catch { return null; }
}

function writeStorage(key: string, value: string) {
  try { localStorage.setItem(key, value); } catch { /* Storage can be disabled by browser policy. */ }
}

export function normaliseAccent(value: string) {
  const candidate = value.trim().toUpperCase();
  return accentPattern.test(candidate) ? candidate : null;
}

function mixChannel(channel: number, target: number, amount: number) {
  return Math.round(channel + (target - channel) * amount);
}

function rgbFromHex(value: string) {
  return [
    Number.parseInt(value.slice(1, 3), 16),
    Number.parseInt(value.slice(3, 5), 16),
    Number.parseInt(value.slice(5, 7), 16),
  ] as const;
}

function mixRGB(source: RGB, target: RGB, amount: number): RGB {
  return [
    mixChannel(source[0], target[0], amount),
    mixChannel(source[1], target[1], amount),
    mixChannel(source[2], target[2], amount),
  ];
}

function rgb(channels: RGB, alpha?: number) {
  return alpha === undefined
    ? `rgb(${channels[0]} ${channels[1]} ${channels[2]})`
    : `rgb(${channels[0]} ${channels[1]} ${channels[2]} / ${alpha})`;
}

function luminance(channels: readonly number[]) {
  const [red, green, blue] = channels.map((channel) => {
    const value = channel / 255;
    return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * red + 0.7152 * green + 0.0722 * blue;
}

function contrast(left: readonly number[], right: readonly number[]) {
  const brighter = Math.max(luminance(left), luminance(right));
  const darker = Math.min(luminance(left), luminance(right));
  return (brighter + 0.05) / (darker + 0.05);
}

function hexFromRGB(channels: readonly number[]) {
  return `#${channels.map((channel) => Math.max(0, Math.min(255, channel)).toString(16).padStart(2, '0')).join('')}`.toUpperCase();
}

export function safeNodeFlowAccent(value: string) {
  const accent = normaliseAccent(value);
  if (!accent) return null;
  const graphite = [7, 17, 15] as const;
  const original = rgbFromHex(accent);
  if (contrast(original, graphite) >= 4.5) return accent;
  for (let step = 1; step <= 40; step += 1) {
    const amount = step / 40;
    const candidate = original.map((channel) => mixChannel(channel, 255, amount));
    if (contrast(candidate, graphite) >= 4.5) return hexFromRGB(candidate);
  }
  return '#FFFFFF';
}

function lightSurfaceAccent(value: string) {
  const original = rgbFromHex(value);
  const white = [255, 255, 255] as const;
  if (contrast(original, white) >= 4.5) return value;
  for (let step = 1; step <= 40; step += 1) {
    const amount = step / 40;
    const candidate = original.map((channel) => mixChannel(channel, 0, amount));
    if (contrast(candidate, white) >= 4.5) return hexFromRGB(candidate);
  }
  return '#000000';
}

function updateAccentVariables() {
  const root = document.documentElement;
  const light = root.dataset.nfResolvedTheme === 'light';
  const seed = rgbFromHex(activeAccent);
  const displayedAccent = light
    ? lightSurfaceAccent(activeAccent)
    : safeNodeFlowAccent(activeAccent) ?? defaultAccent;
  const accent = rgbFromHex(displayedAccent);
  const black: RGB = [0, 0, 0];
  const white: RGB = [255, 255, 255];
  // Primary actions always use white copy. Darken bright custom accents only
  // as much as needed to keep the button readable in every theme.
  const primaryAccent = rgbFromHex(lightSurfaceAccent(displayedAccent));
  const primaryHover = mixRGB(primaryAccent, black, 0.1);

  const palette = light ? {
    canvas: mixRGB([246, 248, 247], seed, 0.05),
    sidebar: mixRGB([241, 245, 243], seed, 0.07),
    canvasRaised: mixRGB([251, 252, 251], seed, 0.03),
    field: mixRGB([248, 250, 249], seed, 0.05),
    surface: mixRGB(white, seed, 0.018),
    surfaceSoft: mixRGB([248, 250, 249], seed, 0.045),
    surfaceRaised: mixRGB([242, 246, 244], seed, 0.075),
    surfaceActive: mixRGB([234, 240, 237], seed, 0.12),
    borderTone: mixRGB(seed, [35, 48, 41], 0.68),
    text: mixRGB([18, 30, 24], seed, 0.025),
    textSecondary: mixRGB([79, 96, 86], seed, 0.045),
    textTertiary: mixRGB([111, 126, 117], seed, 0.05),
    textDisabled: mixRGB([149, 160, 153], seed, 0.045),
  } : {
    canvas: mixRGB([4, 8, 8], seed, 0.06),
    sidebar: mixRGB([4, 8, 8], seed, 0.05),
    canvasRaised: mixRGB([6, 12, 11], seed, 0.075),
    field: mixRGB([5, 11, 10], seed, 0.08),
    surface: mixRGB([7, 14, 13], seed, 0.095),
    surfaceSoft: mixRGB([9, 16, 15], seed, 0.115),
    surfaceRaised: mixRGB([11, 19, 17], seed, 0.14),
    surfaceActive: mixRGB([14, 23, 20], seed, 0.19),
    borderTone: mixRGB(seed, [207, 220, 212], 0.58),
    text: mixRGB([236, 242, 239], seed, 0.025),
    textSecondary: mixRGB([177, 190, 183], seed, 0.05),
    textTertiary: mixRGB([137, 151, 143], seed, 0.06),
    textDisabled: mixRGB([101, 113, 106], seed, 0.055),
  };

  const strong = mixRGB(accent, black, light ? 0.14 : 0.18);
  const accentRamp = [
    mixRGB(accent, white, 0.94),
    mixRGB(accent, white, 0.86),
    mixRGB(accent, white, 0.72),
    mixRGB(accent, white, 0.55),
    mixRGB(accent, white, 0.36),
    mixRGB(accent, white, 0.18),
    accent,
    mixRGB(accent, black, 0.12),
    mixRGB(accent, black, 0.25),
    mixRGB(accent, black, 0.38),
  ];

  const variables: Record<string, string> = {
    '--nf-canvas': rgb(palette.canvas),
    '--nf-sidebar': rgb(palette.sidebar),
    '--nf-canvas-raised': rgb(palette.canvasRaised),
    '--nf-field': rgb(palette.field),
    '--nf-surface': rgb(palette.surface),
    '--nf-surface-soft': rgb(palette.surfaceSoft),
    '--nf-surface-raised': rgb(palette.surfaceRaised),
    '--nf-surface-active': rgb(palette.surfaceActive),
    '--nf-overlay': rgb(palette.canvas, light ? 0.94 : 0.96),
    '--nf-border': rgb(palette.borderTone, light ? 0.22 : 0.2),
    '--nf-border-soft': rgb(palette.borderTone, light ? 0.13 : 0.12),
    '--nf-border-strong': rgb(accent, light ? 0.42 : 0.4),
    '--nf-text': rgb(palette.text),
    '--nf-text-secondary': rgb(palette.textSecondary),
    '--nf-text-tertiary': rgb(palette.textTertiary),
    '--nf-text-disabled': rgb(palette.textDisabled),
    '--nf-accent': displayedAccent,
    '--nf-accent-strong': rgb(strong),
    '--nf-accent-soft': rgb(accent, light ? 0.11 : 0.12),
    '--nf-accent-faint': rgb(accent, light ? 0.055 : 0.06),
    '--nf-primary': rgb(primaryAccent),
    '--nf-primary-hover': rgb(primaryHover),
    '--nf-on-accent': '#FFFFFF',
    '--mantine-primary-color-filled': rgb(primaryAccent),
    '--mantine-primary-color-filled-hover': rgb(primaryHover),
    '--mantine-primary-color-light': rgb(accent, light ? 0.1 : 0.12),
    '--mantine-primary-color-light-hover': rgb(accent, light ? 0.15 : 0.17),
    '--mantine-primary-color-light-color': displayedAccent,
    '--mantine-primary-color-contrast': light ? '#FFFFFF' : '#06100C',
    '--mantine-color-nodeflow-filled': rgb(primaryAccent),
    '--mantine-color-nodeflow-filled-hover': rgb(primaryHover),
    '--mantine-color-nodeflow-light': rgb(accent, light ? 0.1 : 0.12),
    '--mantine-color-nodeflow-light-hover': rgb(accent, light ? 0.15 : 0.17),
    '--mantine-color-nodeflow-light-color': displayedAccent,
    '--mantine-color-nodeflow-outline': displayedAccent,
    '--mantine-color-nodeflow-outline-hover': rgb(accent, light ? 0.08 : 0.1),
  };
  accentRamp.forEach((tone, index) => {
    variables[`--mantine-color-nodeflow-${index}`] = rgb(tone);
  });
  Object.entries(variables).forEach(([name, value]) => root.style.setProperty(name, value));
  window.dispatchEvent(new Event('nodeflow:appearance'));
}

function rgba(channels: readonly number[], alpha: number) {
  return `rgba(${channels[0]}, ${channels[1]}, ${channels[2]}, ${alpha})`;
}

export function nodeFlowChartTheme() {
  const light = document.documentElement.dataset.nfResolvedTheme === 'light';
  const primary = light ? lightSurfaceAccent(activeAccent) : safeNodeFlowAccent(activeAccent) ?? defaultAccent;
  const primaryRGB = rgbFromHex(primary);
  const secondaryRGB = primaryRGB.map((channel) => mixChannel(channel, light ? 0 : 255, light ? 0.22 : 0.38));
  return {
    primary,
    secondary: hexFromRGB(secondaryRGB),
    primaryAreaTop: rgba(primaryRGB, light ? 0.22 : 0.33),
    primaryAreaBottom: rgba(primaryRGB, light ? 0.015 : 0.02),
    secondaryAreaTop: rgba(secondaryRGB, light ? 0.12 : 0.18),
    secondaryAreaBottom: rgba(secondaryRGB, 0),
    axisText: light ? 'rgba(20, 35, 26, .62)' : 'rgba(220, 231, 223, .56)',
    axisLine: light ? 'rgba(48, 78, 61, .18)' : 'rgba(191, 211, 198, .12)',
    splitLine: light ? 'rgba(48, 78, 61, .12)' : 'rgba(191, 211, 198, .09)',
    pointer: light ? 'rgba(20, 35, 26, .48)' : 'rgba(221, 234, 225, .55)',
  };
}

export function applyNodeFlowAccent(value: string, persist = false) {
  const accent = normaliseAccent(value);
  if (!accent) return false;
  const root = document.documentElement;
  activeAccent = accent;
  root.dataset.nfAccent = accent;
  updateAccentVariables();
  if (persist) writeStorage(accentStorageKey, accent);
  return true;
}

function resolveSystemTheme() {
  return window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
}

function updateResolvedTheme() {
  document.documentElement.dataset.nfResolvedTheme = activeTheme === 'system' ? resolveSystemTheme() : 'dark';
  updateAccentVariables();
}

export function applyNodeFlowTheme(theme: PanelTheme, persist = false) {
  activeTheme = theme;
  document.documentElement.dataset.nfTheme = theme;
  updateResolvedTheme();
  if (persist) writeStorage(themeStorageKey, theme);
  if (!systemThemeListenerInstalled && window.matchMedia) {
    window.matchMedia('(prefers-color-scheme: light)').addEventListener('change', updateResolvedTheme);
    systemThemeListenerInstalled = true;
  }
}

export function applyNodeFlowAppearance(settings: Pick<PanelSettings, 'accent' | 'theme'>, persist = false) {
  applyNodeFlowAccent(settings.accent, persist);
  applyNodeFlowTheme(settings.theme, persist);
}

export function initialiseNodeFlowAppearance(demo: boolean) {
  if (demo) {
    // Demo can preview the system (light/dark) theme: ?demo=1&theme=system.
    const requested = new URLSearchParams(window.location.search).get('theme');
    applyNodeFlowAccent(defaultAccent);
    applyNodeFlowTheme(requested === 'system' ? 'system' : 'rose');
    return;
  }
  applyNodeFlowAccent(readStorage(accentStorageKey) ?? defaultAccent);
  const cachedTheme = readStorage(themeStorageKey);
  const supportedThemes: PanelTheme[] = ['dark', 'green', 'rose', 'cyan', 'amber', 'system'];
  applyNodeFlowTheme(supportedThemes.includes(cachedTheme as PanelTheme) ? cachedTheme as PanelTheme : 'rose');
  void fetch('/api/v1/settings', { credentials: 'same-origin' })
    .then(async (response) => response.ok ? response.json() as Promise<PanelSettings> : null)
    .then((settings) => { if (settings) applyNodeFlowAppearance(settings, true); })
    .catch(() => undefined);
}

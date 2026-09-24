export function isInteractiveRowTarget(target: EventTarget | null, row: Element): boolean {
  if (!(target instanceof Element)) return false;
  const interactive = target.closest('a, button, input, select, textarea, [role="button"], [role="link"], [data-row-action]');
  return Boolean(interactive && interactive !== row);
}

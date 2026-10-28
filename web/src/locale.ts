const DISPLAY_LOCALES = navigator.languages?.length ? [...navigator.languages] : [navigator.language || 'en'];
const REGION_NAMES = typeof Intl.DisplayNames === 'function' ? new Intl.DisplayNames(DISPLAY_LOCALES, { type: 'region' }) : null;
const RELATIVE_TIME = new Intl.RelativeTimeFormat(DISPLAY_LOCALES, { numeric: 'auto' });

export const regionName = (code?: string, fallback?: string): string | null => {
  const normalized = code?.trim().toUpperCase();
  if (!normalized || !/^[A-Z]{2}$/.test(normalized)) return fallback ?? null;
  try {
    const name = REGION_NAMES?.of(normalized);
    return name && name !== normalized && name !== 'Unknown Region' ? name : fallback ?? null;
  } catch {
    return fallback ?? null;
  }
};

export const formatRelativeTime = (seconds: number): string => {
  if (seconds < 45) return RELATIVE_TIME.format(0, 'second');
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return RELATIVE_TIME.format(-minutes, 'minute');
  const hours = Math.round(minutes / 60);
  if (hours < 48) return RELATIVE_TIME.format(-hours, 'hour');
  return RELATIVE_TIME.format(-Math.round(hours / 24), 'day');
};

export const formatDateTime = (value?: string): string | null => {
  if (!value) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date.toLocaleString(DISPLAY_LOCALES, { dateStyle: 'medium', timeStyle: 'short' });
};

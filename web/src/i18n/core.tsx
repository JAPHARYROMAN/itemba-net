import React, { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import { en } from './en';
import { sw } from './sw';
import { ar } from './ar';

// The console's message catalog. English is the source of truth: every key
// exists there, and the other catalogs are typed against it, so a missing or
// misspelled key fails the build rather than falling back silently at
// runtime. Server-composed prose (provider details, descriptions, evidence
// details) stays as the server sent it; this catalog covers the interface
// the browser composes itself.

export interface PluralForms {
  zero?: string;
  one?: string;
  two?: string;
  few?: string;
  many?: string;
  other: string;
}

export type MessageValue = string | PluralForms;
export type Messages = { [K in keyof typeof en]: MessageValue };
export type MessageKey = keyof Messages;
export type Vars = Record<string, string | number>;
export type Translate = (key: MessageKey, vars?: Vars) => string;

export const LOCALES = [
  { id: 'en', label: 'English', dir: 'ltr' },
  { id: 'sw', label: 'Kiswahili', dir: 'ltr' },
  { id: 'ar', label: 'العربية', dir: 'rtl' },
] as const;

export type Locale = (typeof LOCALES)[number]['id'];
export type Direction = 'ltr' | 'rtl';

const CATALOGS: Record<Locale, Messages> = { en, sw, ar };
const LOCALE_STORAGE_KEY = 'itemba.locale';

const isLocale = (value: string | null | undefined): value is Locale =>
  LOCALES.some((locale) => locale.id === value);

// The operator's last choice wins; otherwise the browser's first preference
// that this console speaks; otherwise English. A locale is matched on its
// primary language tag, so ar-EG picks the Arabic catalog.
const detectLocale = (): Locale => {
  try {
    const stored = localStorage.getItem(LOCALE_STORAGE_KEY);
    if (isLocale(stored)) return stored;
  } catch {
    // Storage may be unavailable; detection falls through to the browser.
  }
  const languages = navigator.languages?.length ? navigator.languages : [navigator.language || 'en'];
  for (const language of languages) {
    const primary = language.toLowerCase().split(/[-_]/)[0];
    if (isLocale(primary)) return primary;
  }
  return 'en';
};

const interpolate = (message: string, vars?: Vars): string => {
  if (!vars) return message;
  return message.replace(/\{(\w+)\}/g, (match, name: string) => {
    const value = vars[name];
    return value === undefined ? match : String(value);
  });
};

export interface I18nValue {
  locale: Locale;
  dir: Direction;
  setLocale: (locale: Locale) => void;
  t: Translate;
  // Intl formatters, built for the chosen locale rather than the browser's.
  formatDateTime: (value?: string) => string | null;
  formatRelativeTime: (seconds: number) => string;
  formatNumber: (value: number) => string;
  regionName: (code?: string, fallback?: string) => string | null;
}

const I18nContext = createContext<I18nValue | null>(null);

export const I18nProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [locale, setLocaleState] = useState<Locale>(detectLocale);
  const [formatters, setFormatters] = useState(() => buildFormatters(locale));

  const setLocale = useCallback((next: Locale) => {
    setLocaleState(next);
    setFormatters(buildFormatters(next));
  }, []);

  useEffect(() => {
    const meta = LOCALES.find((entry) => entry.id === locale) ?? LOCALES[0];
    document.documentElement.lang = locale;
    document.documentElement.dir = meta.dir;
    try {
      localStorage.setItem(LOCALE_STORAGE_KEY, locale);
    } catch {
      // Storage may be unavailable; the choice lasts for this page view.
    }
  }, [locale]);

  const t = useCallback<Translate>((key, vars) => {
    const value = CATALOGS[locale][key] ?? CATALOGS.en[key];
    if (value === undefined) return key;
    if (typeof value === 'string') return interpolate(value, vars);
    const category = formatters.pluralRules.select(Number(vars?.count ?? 0));
    return interpolate(value[category as keyof PluralForms] ?? value.other, vars);
  }, [locale, formatters]);

  const value = useMemo<I18nValue>(() => ({
    locale,
    dir: LOCALES.find((entry) => entry.id === locale)?.dir ?? 'ltr',
    setLocale,
    t,
    ...formatters,
  }), [locale, setLocale, t, formatters]);

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
};

export const useI18n = (): I18nValue => {
  const value = useContext(I18nContext);
  if (!value) throw new Error('useI18n must be used inside I18nProvider');
  return value;
};

function buildFormatters(locale: Locale) {
  const displayNames = typeof Intl.DisplayNames === 'function' ? new Intl.DisplayNames([locale], { type: 'region' }) : null;
  const unknownRegion = typeof Intl.DisplayNames === 'function' ? new Intl.DisplayNames([locale], { type: 'region' }).of('ZZ') ?? 'Unknown Region' : 'Unknown Region';
  const relativeTime = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' });
  const dateTime = new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' });
  const number = new Intl.NumberFormat(locale);
  const pluralRules = new Intl.PluralRules(locale);

  const regionName = (code?: string, fallback?: string): string | null => {
    const normalized = code?.trim().toUpperCase();
    if (!normalized || !/^[A-Z]{2}$/.test(normalized)) return fallback ?? null;
    try {
      const name = displayNames?.of(normalized);
      return name && name !== normalized && name !== unknownRegion ? name : fallback ?? null;
    } catch {
      return fallback ?? null;
    }
  };

  const formatRelativeTime = (seconds: number): string => {
    // The thresholds stay seconds-based; the wording comes from Intl, so a
    // locale decides for itself whether "now" is used.
    if (seconds < 45) return relativeTime.format(0, 'second');
    const minutes = Math.round(seconds / 60);
    if (minutes < 60) return relativeTime.format(-minutes, 'minute');
    const hours = Math.round(minutes / 60);
    if (hours < 48) return relativeTime.format(-hours, 'hour');
    return relativeTime.format(-Math.round(hours / 24), 'day');
  };

  const formatDateTime = (value?: string): string | null => {
    if (!value) return null;
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? null : dateTime.format(date);
  };

  return {
    regionName,
    formatRelativeTime,
    formatDateTime,
    formatNumber: (value: number) => number.format(value),
    pluralRules,
  };
}

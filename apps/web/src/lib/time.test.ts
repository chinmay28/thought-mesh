import { describe, expect, it } from 'vitest';
import { relativeTime, todayStamp } from './time.ts';

describe('relativeTime', () => {
  const now = Date.UTC(2026, 7, 23, 12, 0, 0);
  it('buckets recent times', () => {
    expect(relativeTime(now - 10_000, now)).toBe('just now');
    expect(relativeTime(now - 5 * 60_000, now)).toBe('5m ago');
    expect(relativeTime(now - 3 * 3_600_000, now)).toBe('3h ago');
    expect(relativeTime(now - 2 * 86_400_000, now)).toBe('2d ago');
  });
  it('falls back to a date for older times', () => {
    const out = relativeTime(now - 30 * 86_400_000, now);
    expect(out).not.toContain('ago');
  });
});

describe('todayStamp', () => {
  it('formats the local date as YYYY-MM-DD', () => {
    expect(todayStamp(new Date(2026, 0, 5))).toBe('2026-01-05');
  });
});

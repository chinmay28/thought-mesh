import { afterEach, describe, expect, it, vi } from 'vitest';
import { copyText } from './clipboard.ts';

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('copyText', () => {
  it('uses the Clipboard API in a secure context', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('isSecureContext', true);
    vi.stubGlobal('navigator', { clipboard: { writeText } });

    await copyText('hello');
    expect(writeText).toHaveBeenCalledWith('hello');
  });

  it('falls back to execCommand over plain HTTP, and cleans up after itself', async () => {
    vi.stubGlobal('isSecureContext', false);
    let copied = '';
    document.execCommand = vi.fn(() => {
      copied = document.querySelector('textarea')?.value ?? '';
      return true;
    });

    await copyText('from the LAN');
    expect(copied).toBe('from the LAN');
    expect(document.querySelector('textarea')).toBeNull();
  });

  it('reports a refused copy', async () => {
    vi.stubGlobal('isSecureContext', false);
    document.execCommand = vi.fn(() => false);
    await expect(copyText('x')).rejects.toThrow(/refused/);
  });
});

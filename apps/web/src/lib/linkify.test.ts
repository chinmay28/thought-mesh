import { describe, expect, it } from 'vitest';
import { codeFenceSpans, fenceAsCode, toggleWikilink, wikilinkSpans } from './linkify.ts';

/** Select the first occurrence of `needle` in `src`. */
function select(src: string, needle: string): [number, number] {
  const at = src.indexOf(needle);
  if (at < 0) throw new Error(`"${needle}" not in source`);
  return [at, at + needle.length];
}

describe('toggleWikilink', () => {
  it('wraps the selected words in brackets', () => {
    const src = 'Swing XLK with a SATA cash sweep.';
    const edit = toggleWikilink(src, ...select(src, 'SATA cash sweep'));
    expect(edit).toEqual({
      kind: 'link',
      content: 'Swing XLK with a [[SATA cash sweep]].',
      target: 'SATA cash sweep',
      span: { start: 17, end: 36 },
    });
  });

  it('widens a partial word to the whole word', () => {
    const src = 'Swing XLK daily';
    const edit = toggleWikilink(src, ...select(src, 'LK da'));
    expect(edit.kind === 'link' && edit.content).toBe('Swing [[XLK daily]]');
  });

  it('trims spaces, punctuation and emphasis around the selection', () => {
    const src = 'Run **SATA sweep**, then stop.';
    const edit = toggleWikilink(src, ...select(src, ' **SATA sweep**, '));
    expect(edit.kind === 'link' && edit.content).toBe('Run **[[SATA sweep]]**, then stop.');
  });

  it('accepts a selection given end-first', () => {
    const src = 'alpha beta';
    const [start, end] = select(src, 'beta');
    expect(toggleWikilink(src, end, start)).toMatchObject({ kind: 'link', target: 'beta' });
  });

  it('ignores a selection of only spaces or punctuation', () => {
    expect(toggleWikilink('a -- b', 1, 5)).toEqual({ kind: 'none' });
    expect(toggleWikilink('abc', 1, 1)).toEqual({ kind: 'none' });
  });

  it('removes a link when the selection is inside it, keeping the shown text', () => {
    const src = 'See [[Investing/SATA|SATA]] now.';
    const edit = toggleWikilink(src, ...select(src, 'SATA]]'));
    expect(edit).toEqual({
      kind: 'unlink',
      content: 'See SATA now.',
      target: 'SATA',
      span: { start: 4, end: 8 },
    });
  });

  it('refuses a selection that runs into an existing link', () => {
    const src = 'the [[XLK]] swing';
    expect(toggleWikilink(src, ...select(src, 'XLK]] swing')).kind).toBe('refused');
  });

  it('refuses a selection across lines', () => {
    const src = 'first line\nsecond line';
    expect(toggleWikilink(src, ...select(src, 'line\nsecond')).kind).toBe('refused');
  });

  it('refuses text that cannot be a note name', () => {
    const src = 'at 12:00 PM sharp';
    const edit = toggleWikilink(src, ...select(src, '12:00 PM'));
    expect(edit).toMatchObject({ kind: 'refused' });
    expect(edit.kind === 'refused' && edit.reason).toContain('“:”');
  });

  it('refuses inside a fenced code block', () => {
    const src = 'text\n```\ncode here\n```\nmore';
    expect(toggleWikilink(src, ...select(src, 'code here')).kind).toBe('refused');
  });

  it('clamps out-of-range offsets', () => {
    expect(toggleWikilink('word', -5, 99)).toMatchObject({ kind: 'link', content: '[[word]]' });
  });
});

describe('wikilinkSpans', () => {
  it('finds links outside code fences only', () => {
    const src = '[[A]] x\n```\n[[B]]\n```\n[[C|see]]';
    expect(wikilinkSpans(src).map((s) => src.slice(s.start, s.end))).toEqual(['[[A]]', '[[C|see]]']);
  });
});

describe('codeFenceSpans', () => {
  it('runs an unclosed fence to the end', () => {
    const src = 'a\n~~~\nb';
    expect(codeFenceSpans(src)).toEqual([{ start: 2, end: src.length }]);
  });
});

describe('fenceAsCode', () => {
  it('wraps text in a three-backtick fence', () => {
    expect(fenceAsCode('hello')).toBe('```\nhello\n```\n');
  });

  it('outgrows any backtick run inside, so the content cannot close it', () => {
    expect(fenceAsCode('a\n````\nb\n')).toBe('`````\na\n````\nb\n`````\n');
  });
});

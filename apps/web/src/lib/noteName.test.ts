import { describe, expect, it } from 'vitest';
import { noteNameFromBody } from './noteName.ts';

describe('noteNameFromBody', () => {
  it('takes the first line with something on it', () => {
    expect(noteNameFromBody('\n\n  Buy oat milk\nand bread\n')).toBe('Buy oat milk');
  });

  it('strips the markdown a file name would not carry', () => {
    expect(noteNameFromBody('# Weekly review')).toBe('Weekly review');
    expect(noteNameFromBody('## Weekly review ##')).toBe('Weekly review');
    expect(noteNameFromBody('- [ ] Call the vet')).toBe('Call the vet');
    expect(noteNameFromBody('> **Quoted** thought')).toBe('Quoted thought');
    expect(noteNameFromBody('1. First step')).toBe('First step');
  });

  it('keeps the words a link displays', () => {
    expect(noteNameFromBody('Ideas for [[Project Zed]]')).toBe('Ideas for Project Zed');
    expect(noteNameFromBody('[[projects/zed|Zed]] notes')).toBe('Zed notes');
    expect(noteNameFromBody('Read [the paper](https://x.test/p)')).toBe('Read the paper');
  });

  it('truncates a long first line at a word boundary', () => {
    const name = noteNameFromBody(`${'word '.repeat(40)}end`);
    expect(name.length).toBeLessThanOrEqual(60);
    expect(name.endsWith('word')).toBe(true);
  });

  it('falls back to a timestamp when there is nothing to name it after', () => {
    expect(noteNameFromBody('   \n\n', new Date(2026, 7, 24, 9, 4))).toBe(
      'Note 2026-08-24 09.04',
    );
    // A body of nothing but markup is just as unnamed.
    expect(noteNameFromBody('###   ', new Date(2026, 0, 5, 17, 30))).toBe(
      'Note 2026-01-05 17.30',
    );
  });
});

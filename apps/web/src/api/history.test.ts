import { describe, expect, it } from 'vitest';
import { historyDetail, type HistoryCommit } from './history.ts';

function commit(over: Partial<HistoryCommit>): HistoryCommit {
  return {
    ref: 'abc123',
    short: 'abc123',
    subject: '',
    body: '',
    kind: 'edit',
    author: 'Thought Mesh',
    at_ms: 0,
    ...over,
  };
}

describe('historyDetail', () => {
  // Commit subjects carry the time because `git log` has no column of dates
  // beside them. This list does, so repeating it is noise.
  it('strips the timestamp the list already shows', () => {
    expect(historyDetail(commit({ subject: 'Notes edited at 2026-08-25 07:47' }))).toBe(
      'Notes edited',
    );
    expect(
      historyDetail(commit({ kind: 'local', subject: 'Local edits before sync at 2026-08-25 07:47' })),
    ).toBe('Local edits before sync');
    expect(
      historyDetail(
        commit({ kind: 'conflict', subject: "Resolve Idea.md — kept this server's version, at 2026-08-25 07:47" }),
      ),
    ).toBe("Resolve Idea.md — kept this server's version");
  });

  // What's left of "Checkpoint at <time>" is exactly the kind label, so showing
  // it would print the same word twice on one row.
  it('drops a subject that only repeats the kind label', () => {
    expect(historyDetail(commit({ kind: 'checkpoint', subject: 'Checkpoint at 2026-08-25 07:47' }))).toBe('');
  });

  it('keeps a subject that carries real information', () => {
    expect(
      historyDetail(commit({ kind: 'sync', subject: 'Sync 2026-08-25 07:47 — 3 up, 1 down' })),
    ).toBe('Sync 2026-08-25 07:47 — 3 up, 1 down');
    expect(
      historyDetail(commit({ kind: 'rollback', subject: 'Roll back to b609185 — Checkpoint at 2026-08-25 07:43' })),
    ).toBe('Roll back to b609185 — Checkpoint');
  });

  // A commit somebody made with git themselves belongs in this history too.
  it('leaves a hand-written subject alone', () => {
    expect(historyDetail(commit({ kind: '', subject: 'Reorganised the journal' }))).toBe(
      'Reorganised the journal',
    );
  });
});

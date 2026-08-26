import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { FolderPicker } from './FolderPicker.tsx';
import type { Folder } from '../api/client.ts';

const folder = (path: string, count: number): Folder => ({
  path,
  name: path.split('/').pop() ?? '',
  depth: path.split('/').length,
  count,
  total: count,
});

const known = [folder('Money', 5), folder('Reading/2026', 2), folder('Journal', 9)];

describe('FolderPicker', () => {
  /** The suggestion list only — not the form's own "Move" button. */
  const suggested = () =>
    within(screen.getByRole('list'))
      .getAllByRole('button')
      .map((b) => b.textContent ?? '');

  it('suggests existing folders, most used first', () => {
    render(<FolderPicker value="" known={known} onChange={() => {}} />);
    expect(suggested()[0]).toContain('Journal');
    expect(suggested()[1]).toContain('Money');
  });

  // The box starts pre-filled with the current folder so it can be edited.
  // That text must not filter the list, or a filed note would open the picker
  // on nothing at all — its own folder is excluded and every other one fails
  // the match.
  it('still suggests other folders when the note is already filed', () => {
    render(<FolderPicker value="Money" known={known} onChange={() => {}} />);
    const names = suggested();
    expect(names.some((n) => n.includes('Journal'))).toBe(true);
    expect(names.some((n) => n.includes('Reading/2026'))).toBe(true);
  });

  it('does not suggest the folder the note is already in', () => {
    render(<FolderPicker value="Money" known={known} onChange={() => {}} />);
    expect(suggested().some((n) => n.startsWith('Money'))).toBe(false);
  });

  it('narrows the suggestions once the user types', async () => {
    const user = userEvent.setup();
    render(<FolderPicker value="" known={known} onChange={() => {}} />);
    await user.type(screen.getByLabelText('Folder for this note'), 'read');
    expect(suggested()).toHaveLength(1);
    expect(suggested()[0]).toContain('Reading/2026');
  });

  it('offers unfiling only when the note is filed', () => {
    const { unmount } = render(<FolderPicker value="Money" known={known} onChange={() => {}} />);
    expect(screen.getByRole('button', { name: 'Unfiled' })).toBeInTheDocument();
    unmount();
    render(<FolderPicker value="" known={known} onChange={() => {}} />);
    expect(screen.queryByRole('button', { name: 'Unfiled' })).toBeNull();
  });

  it('reports a typed folder, trimmed of stray slashes', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<FolderPicker value="" known={known} onChange={onChange} />);

    await user.type(screen.getByLabelText('Folder for this note'), '/Ideas/');
    await user.click(screen.getByRole('button', { name: 'Move' }));

    expect(onChange).toHaveBeenCalledWith('Ideas');
  });

  it('will not move a note to the folder it is already in', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<FolderPicker value="Money" known={known} onChange={onChange} />);

    // Same folder, different case — still the same folder, so nothing to do.
    const input = screen.getByLabelText('Folder for this note');
    await user.clear(input);
    await user.type(input, 'money');
    expect(screen.getByRole('button', { name: 'Move' })).toBeDisabled();
    expect(onChange).not.toHaveBeenCalled();
  });

  it('picks a suggestion straight through', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<FolderPicker value="" known={known} onChange={onChange} />);

    await user.click(screen.getByRole('button', { name: /Reading\/2026/ }));
    expect(onChange).toHaveBeenCalledWith('Reading/2026');
  });
});

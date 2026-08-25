import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { CategoryPicker } from './CategoryPicker.tsx';
import type { Category } from '../api/client.ts';

const known: Category[] = [
  { name: 'Work', count: 12 },
  { name: 'Ideas', count: 4 },
  { name: 'Reading list', count: 1 },
];

function picker(value: string[], onChange = vi.fn()) {
  render(<CategoryPicker value={value} known={known} onChange={onChange} />);
  return onChange;
}

describe('CategoryPicker', () => {
  it('adds what was typed, collapsing stray whitespace', () => {
    const onChange = picker([]);
    fireEvent.change(screen.getByLabelText('Add a category'), {
      target: { value: '  reading   list  ' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Add' }));
    expect(onChange).toHaveBeenCalledWith(['reading list']);
  });

  // A category the note already has isn't an error, it's a no-op — the user
  // asked for a state that already holds.
  it('ignores a category the note already carries, whatever the casing', () => {
    const onChange = picker(['Work']);
    fireEvent.change(screen.getByLabelText('Add a category'), { target: { value: 'work' } });
    fireEvent.click(screen.getByRole('button', { name: 'Add' }));
    expect(onChange).not.toHaveBeenCalled();
  });

  it('removes a chip', () => {
    const onChange = picker(['Work', 'Ideas']);
    fireEvent.click(screen.getByRole('button', { name: 'Remove category Work' }));
    expect(onChange).toHaveBeenCalledWith(['Ideas']);
  });

  // The convention for a field made of chips, and the only way to undo without
  // aiming at a small "×".
  it('removes the last chip on backspace in an empty box', () => {
    const onChange = picker(['Work', 'Ideas']);
    fireEvent.keyDown(screen.getByLabelText('Add a category'), { key: 'Backspace' });
    expect(onChange).toHaveBeenCalledWith(['Work']);
  });

  it('leaves the chips alone when backspace is deleting typed text', () => {
    const onChange = picker(['Work']);
    const input = screen.getByLabelText('Add a category');
    fireEvent.change(input, { target: { value: 'x' } });
    fireEvent.keyDown(input, { key: 'Backspace' });
    expect(onChange).not.toHaveBeenCalled();
  });

  it('suggests what the vault already uses, most-used first, minus what this note has', () => {
    picker(['Work']);
    fireEvent.focus(screen.getByLabelText('Add a category'));
    const suggestions = screen
      .getAllByRole('button')
      .map((b) => b.textContent ?? '')
      .filter((text) => text.startsWith('Ideas') || text.startsWith('Reading list'));
    expect(suggestions[0]).toContain('Ideas');
    expect(suggestions[1]).toContain('Reading list');
    expect(screen.queryByRole('button', { name: /^Work12$/ })).toBeNull();
  });

  it('narrows the suggestions to what has been typed', () => {
    picker([]);
    fireEvent.change(screen.getByLabelText('Add a category'), { target: { value: 'read' } });
    expect(screen.getByRole('button', { name: /Reading list/ })).toBeTruthy();
    expect(screen.queryByRole('button', { name: /Ideas/ })).toBeNull();
  });

  it('says so when there are no categories yet', () => {
    picker([]);
    expect(screen.getByText('No categories yet')).toBeTruthy();
  });
});

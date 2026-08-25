import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Menu } from './Menu.tsx';

describe('Menu', () => {
  it('keeps its items out of the way until the trigger is pressed', async () => {
    const user = userEvent.setup();
    render(<Menu label="More actions" items={[{ label: 'Rename', onSelect: () => {} }]} />);

    expect(screen.queryByRole('menuitem', { name: 'Rename' })).toBeNull();
    await user.click(screen.getByRole('button', { name: 'More actions' }));
    expect(screen.getByRole('menuitem', { name: 'Rename' })).toBeInTheDocument();
  });

  it('runs the chosen action and closes', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(<Menu label="More actions" items={[{ label: 'Delete', onSelect, danger: true }]} />);

    await user.click(screen.getByRole('button', { name: 'More actions' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete' }));

    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('menuitem', { name: 'Delete' })).toBeNull();
  });

  it('announces a toggle item as checkable, with its state', async () => {
    const user = userEvent.setup();
    render(<Menu label="More actions" items={[{ label: 'History', checked: true, onSelect: () => {} }]} />);

    await user.click(screen.getByRole('button', { name: 'More actions' }));
    expect(screen.getByRole('menuitemcheckbox', { name: /History/ })).toBeChecked();
  });

  it('closes on Escape and hands focus back to the trigger', async () => {
    const user = userEvent.setup();
    render(<Menu label="More actions" items={[{ label: 'Rename', onSelect: () => {} }]} />);

    const trigger = screen.getByRole('button', { name: 'More actions' });
    await user.click(trigger);
    await user.keyboard('{Escape}');

    expect(screen.queryByRole('menuitem', { name: 'Rename' })).toBeNull();
    expect(trigger).toHaveFocus();
  });

  it('closes when a press lands outside it', async () => {
    const user = userEvent.setup();
    render(
      <div>
        <Menu label="More actions" items={[{ label: 'Rename', onSelect: () => {} }]} />
        <button type="button">Elsewhere</button>
      </div>,
    );

    await user.click(screen.getByRole('button', { name: 'More actions' }));
    await user.click(screen.getByRole('button', { name: 'Elsewhere' }));

    expect(screen.queryByRole('menuitem', { name: 'Rename' })).toBeNull();
  });
});

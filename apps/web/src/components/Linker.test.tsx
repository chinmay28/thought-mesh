import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render } from '@testing-library/react';
import { Linker, SETTLE_MS, selectionOffsets } from './Linker.tsx';

afterEach(() => {
  vi.useRealTimers();
  window.getSelection()?.removeAllRanges();
});

/** Select `[start, end)` of the text inside `root`, walking its text nodes. */
function selectText(root: Node, start: number, end: number) {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const range = document.createRange();
  let pos = 0;
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const len = n.textContent?.length ?? 0;
    if (start >= pos && start <= pos + len) range.setStart(n, start - pos);
    if (end >= pos && end <= pos + len) {
      range.setEnd(n, end - pos);
      break;
    }
    pos += len;
  }
  const sel = window.getSelection()!;
  sel.removeAllRanges();
  sel.addRange(range);
}

describe('Linker', () => {
  const src = 'Swing [[XLK]] with a SATA cash sweep';

  it('shows the source verbatim, highlighting links by whether they resolve', () => {
    const { container } = render(<Linker content={src} resolves={() => false} onPick={() => {}} />);
    const root = container.querySelector('.linker')!;
    expect(root.textContent).toBe(src);
    expect(root.querySelector('mark.linker__link--new')?.textContent).toBe('[[XLK]]');
  });

  it('maps a selection after a highlighted link to source offsets', () => {
    const { container } = render(<Linker content={src} resolves={() => true} onPick={() => {}} />);
    const root = container.querySelector('.linker')!;
    const at = src.indexOf('SATA');
    selectText(root, at, at + 4);
    expect(selectionOffsets(root, window.getSelection()!)).toEqual([at, at + 4]);
  });

  it('ignores a selection that reaches outside it', () => {
    const { container } = render(
      <div>
        <p>outside</p>
        <Linker content={src} resolves={() => true} onPick={() => {}} />
      </div>,
    );
    selectText(container, 2, 12);
    expect(selectionOffsets(container.querySelector('.linker')!, window.getSelection()!)).toBeNull();
  });

  it('picks on mouse release, and on a touch selection once it settles', () => {
    vi.useFakeTimers();
    const onPick = vi.fn();
    const { container } = render(<Linker content={src} resolves={() => true} onPick={onPick} />);
    const root = container.querySelector('.linker')!;

    selectText(root, 0, 5);
    fireEvent.mouseUp(root);
    expect(onPick).toHaveBeenLastCalledWith(0, 5);
    expect(window.getSelection()!.isCollapsed).toBe(true);

    selectText(root, 21, 25);
    fireEvent(document, new Event('selectionchange'));
    act(() => vi.advanceTimersByTime(SETTLE_MS - 1));
    expect(onPick).toHaveBeenCalledTimes(1);
    act(() => vi.advanceTimersByTime(1));
    expect(onPick).toHaveBeenLastCalledWith(21, 25);
  });
});

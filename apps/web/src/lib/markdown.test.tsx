import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import {
  renderMarkdown,
  splitFrontmatter,
  wikiLabel,
  wikiTarget,
  type RenderOptions,
} from './markdown.tsx';

const opts: RenderOptions = {
  resolve: (target) => (target.toLowerCase() === 'known' ? 'dir/Known.md' : null),
  linkTo: (path) => `/notes/${path}`,
  createTo: (target) => `/new?name=${encodeURIComponent(target)}`,
};

function md(src: string) {
  return render(<MemoryRouter>{renderMarkdown(src, opts)}</MemoryRouter>);
}

describe('wikiTarget / wikiLabel', () => {
  it('strips aliases and headings', () => {
    expect(wikiTarget('Note#Heading|shown')).toBe('Note');
    expect(wikiTarget(' Note ')).toBe('Note');
    expect(wikiLabel('Note|shown text')).toBe('shown text');
    expect(wikiLabel('Note#Heading')).toBe('Note#Heading');
  });
});

describe('renderMarkdown', () => {
  it('renders headings, emphasis and code', () => {
    const { container } = md('# Title\n\nSome **bold** and *italic* and `code`.');
    expect(container.querySelector('h1')).toHaveTextContent('Title');
    expect(container.querySelector('strong')).toHaveTextContent('bold');
    expect(container.querySelector('em')).toHaveTextContent('italic');
    expect(container.querySelector('code')).toHaveTextContent('code');
  });

  it('renders a resolved wikilink as a router link', () => {
    md('go to [[Known]]');
    const link = screen.getByRole('link', { name: 'Known' });
    expect(link).toHaveAttribute('href', '/notes/dir/Known.md');
    expect(link.className).toContain('wikilink');
    expect(link.className).not.toContain('wikilink--new');
  });

  it('renders a missing wikilink as a create link', () => {
    md('see [[Unknown Note]]');
    const link = screen.getByRole('link', { name: 'Unknown Note' });
    expect(link).toHaveAttribute('href', '/new?name=Unknown%20Note');
    expect(link.className).toContain('wikilink--new');
  });

  it('shows the alias for [[target|alias]]', () => {
    md('see [[Known|the known one]]');
    const link = screen.getByRole('link', { name: 'the known one' });
    expect(link).toHaveAttribute('href', '/notes/dir/Known.md');
  });

  it('never renders raw HTML from note content', () => {
    const { container } = md('<script>alert(1)</script> <b>plain</b>');
    expect(container.querySelector('script')).toBeNull();
    expect(container.querySelector('b')).toBeNull();
    expect(container.textContent).toContain('<b>plain</b>');
  });

  it('keeps wikilinks in code blocks literal', () => {
    const { container } = md('```\n[[Known]]\n```');
    expect(container.querySelector('a')).toBeNull();
    expect(container.querySelector('pre code')).toHaveTextContent('[[Known]]');
  });

  it('renders task lists with checkboxes', () => {
    const { container } = md('- [ ] open\n- [x] done');
    const boxes = container.querySelectorAll('input[type=checkbox]');
    expect(boxes).toHaveLength(2);
    expect((boxes[0] as HTMLInputElement).checked).toBe(false);
    expect((boxes[1] as HTMLInputElement).checked).toBe(true);
  });

  it('nests lists by indentation', () => {
    const { container } = md('- a\n  - a1\n- b');
    const outer = container.querySelector('ul')!;
    expect(outer.querySelectorAll(':scope > li')).toHaveLength(2);
    expect(outer.querySelector('li ul li')).toHaveTextContent('a1');
  });

  it('turns single newlines into line breaks inside a paragraph', () => {
    const { container } = md('line one\nline two');
    expect(container.querySelectorAll('p')).toHaveLength(1);
    expect(container.querySelector('p br')).not.toBeNull();
  });

  it('renders blockquotes and rules', () => {
    const { container } = md('> quoted [[Known]]\n\n---');
    expect(container.querySelector('blockquote a')).toHaveTextContent('Known');
    expect(container.querySelector('hr')).not.toBeNull();
  });

  it('renders external links safely', () => {
    md('[docs](https://example.com)');
    const a = screen.getByRole('link', { name: 'docs' });
    expect(a).toHaveAttribute('href', 'https://example.com');
    expect(a).toHaveAttribute('rel', 'noreferrer');
  });
});

describe('frontmatter', () => {
  it('splits a leading YAML block off the body', () => {
    const { frontmatter, body } = splitFrontmatter('---\ncategories: [Ideas]\n---\n\n# Title\n');
    expect(frontmatter).toEqual(['categories: [Ideas]']);
    expect(body).toBe('\n# Title\n');
  });

  it('leaves a lone --- alone: with no closing fence it is a horizontal rule', () => {
    const src = '---\njust a rule above\n';
    expect(splitFrontmatter(src).body).toBe(src);
    expect(splitFrontmatter(src).frontmatter).toEqual([]);
  });

  it('accepts the "..." closing fence YAML also allows', () => {
    expect(splitFrontmatter('---\na: 1\n...\nbody\n').body).toBe('body\n');
  });

  // Categories are metadata, not prose: rendering the block would put a stray
  // rule and a line of YAML at the top of every categorised note.
  it('is not rendered as content', () => {
    const { container } = md('---\ncategories: [Ideas, Work]\n---\n\n# Real title\n');
    expect(container.textContent).not.toContain('categories');
    expect(container.querySelector('h1')?.textContent).toBe('Real title');
    expect(container.querySelector('hr')).toBeNull();
  });

  // …but only at the top level. A `---` inside a blockquote is the rule it
  // looks like.
  it('does not strip a rule nested in a blockquote', () => {
    const { container } = md('> above\n>\n> ---\n>\n> below\n');
    expect(container.querySelector('blockquote hr')).not.toBeNull();
  });
});

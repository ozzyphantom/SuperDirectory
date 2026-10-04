// Writes the Markdown edition of each built page: dist/<page>/index.md.
// Runs after `astro build` (chained in package.json). Served two ways: as a
// static file, or through content negotiation in functions/_middleware.js
// when a client sends `Accept: text/markdown`. This is the self-hosted
// stand-in for Cloudflare's Markdown for Agents (Pro-gated), adapted from
// Personal-Site's scripts/build-md.mjs.
//
// Personal-Site's converter walks its own idioms. This site's pages are plain
// semantic HTML, so this one walks elements generically (headings,
// paragraphs, lists, definition lists, tables, figures, <pre>) and knows four
// idioms of its own:
//   - .cmd        an install command beside its copy button: a fenced block
//   - .ln         one line of the terminal render: one line of a text block
//   - kbd         a key or a combination: `Ctrl+C`, with the general names
//   - .ai-note    the footer's AI-written label: appended to every edition,
//                 so the label travels with the text (UX standard [02.3])
// It skips interaction chrome (buttons, the tab row, the table of contents),
// anything hidden from assistive technology (aria-hidden), anything marked
// data-md-skip, and the Mac-only key names. Teach it any new idiom, or the
// edition silently thins.

import { readFile, writeFile } from 'node:fs/promises';
import { parse } from 'node-html-parser';

const PAGES = ['dist/index.html', 'dist/docs/index.html', 'dist/disclosures/index.html'];

const SKIP_TAGS = new Set(['script', 'style', 'svg', 'button', 'template', 'noscript']);
const BLOCK_TAGS = new Set([
  'p', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'ul', 'ol', 'dl', 'table', 'pre',
  'figure', 'figcaption', 'section', 'div', 'nav', 'header', 'footer', 'main', 'article', 'aside',
]);

const isEl = (n) => n.nodeType === 1;
const tagOf = (n) => n.rawTagName?.toLowerCase();
const has = (n, c) => Boolean(n.classList?.contains(c));
const squash = (s) => s.replace(/\s+/g, ' ');

function skipped(n) {
  if (!isEl(n)) return false;
  if (SKIP_TAGS.has(tagOf(n))) return true;
  if (n.getAttribute('aria-hidden') === 'true') return true;
  if (n.hasAttribute('data-md-skip')) return true;
  return has(n, 'on-mac');
}

const kids = (n) => n.childNodes.filter((c) => !skipped(c));
const abs = (href, base) => (/^(https?:|mailto:)/.test(href) ? href : new URL(href, base).href);

/* Text of an element, minus skipped nodes, whitespace collapsed. */
function plain(n) {
  let out = '';
  for (const c of kids(n)) out += c.nodeType === 3 ? c.text : isEl(c) ? plain(c) : '';
  return squash(out).trim();
}

/* ---- inline ------------------------------------------------------------ */

function inlineNode(n, base) {
  if (n.nodeType === 3) return squash(n.text);
  if (!isEl(n) || skipped(n)) return '';
  const tag = tagOf(n);
  if (tag === 'code' || tag === 'kbd' || tag === 'samp') return `\`${plain(n)}\``;
  if (tag === 'strong' || tag === 'b') return `**${inline(n, base).trim()}**`;
  if (tag === 'em' || tag === 'i') return `*${inline(n, base).trim()}*`;
  if (tag === 'br') return ' ';
  if (tag === 'a') {
    const text = inline(n, base).trim();
    return text ? `[${text}](${abs(n.getAttribute('href'), base)})` : '';
  }
  return inline(n, base);
}

function inline(n, base) {
  return kids(n).map((c) => inlineNode(c, base)).join('');
}

/* ---- blocks ------------------------------------------------------------ */

const indent = (text, pad) => text.split('\n').map((l) => (l ? pad + l : l)).join('\n');

function fence(text, lang) {
  return `\`\`\`${lang}\n${text}\n\`\`\``;
}

/* The blocks inside a container. Loose inline content between blocks
   becomes its own paragraph. */
function blocks(n, base) {
  const out = [];
  let para = '';
  const flush = () => {
    const t = squash(para).trim();
    if (t) out.push(t);
    para = '';
  };
  for (const c of kids(n)) {
    if (c.nodeType === 3) { para += c.text; continue; }
    if (!isEl(c)) continue;
    if (!BLOCK_TAGS.has(tagOf(c))) { para += inlineNode(c, base); continue; }
    flush();
    out.push(...block(c, base));
  }
  flush();
  return out;
}

function block(n, base) {
  const tag = tagOf(n);
  if (/^h[1-6]$/.test(tag)) return [`${'#'.repeat(Number(tag[1]))} ${squash(inline(n, base)).trim()}`];
  if (tag === 'p') {
    const t = squash(inline(n, base)).trim();
    return t ? [t] : [];
  }
  if (tag === 'figcaption') return [`**${plain(n)}**`];
  if (tag === 'ul' || tag === 'ol') return [list(n, base, tag === 'ol')];
  if (tag === 'dl') return [deflist(n, base)];
  if (tag === 'table') return [table(n, base)];
  if (tag === 'pre') return [pre(n)];
  if (has(n, 'cmd')) return [fence(plain(n.querySelector('[data-copy-text]')), 'sh')];
  return blocks(n, base);
}

function list(n, base, ordered) {
  let i = 0;
  const items = kids(n).filter((c) => isEl(c) && tagOf(c) === 'li').map((li) => {
    const marker = ordered ? `${++i}. ` : '- ';
    const pad = ' '.repeat(marker.length);
    const [first = '', ...rest] = blocks(li, base);
    return [marker + indent(first, pad).slice(pad.length), ...rest.map((b) => `\n${indent(b, pad)}`)].join('\n');
  });
  return items.join('\n');
}

/* <dl>: "- term: definition". A <div> may wrap each dt/dd group. A term that
   is all code is not bolded twice over; a term that ends in punctuation gets
   no colon. One-line definitions run on after the term; a block (a list, a
   command) nests under it. */
function deflist(n, base) {
  const rows = [];
  for (const c of kids(n)) {
    if (!isEl(c)) continue;
    for (const d of tagOf(c) === 'div' ? kids(c) : [c]) {
      if (!isEl(d)) continue;
      if (tagOf(d) === 'dt') rows.push({ term: squash(inline(d, base)).trim(), defs: [] });
      else if (tagOf(d) === 'dd' && rows.length) rows[rows.length - 1].defs.push(...blocks(d, base));
    }
  }
  return rows.map(({ term, defs }) => {
    const head = /^(`[^`]*`[,\s]*)+$/.test(term) ? term : `**${term}**`;
    const sep = /[.:?!]$/.test(term) ? ' ' : ': ';
    const inlineDefs = defs.filter((b) => !b.includes('\n')).join(' ');
    const nested = defs.filter((b) => b.includes('\n')).map((b) => `\n${indent(b, '  ')}`);
    return [`- ${head}${inlineDefs ? `${sep}${inlineDefs}` : ''}`, ...nested].join('\n');
  }).join('\n');
}

function table(n, base) {
  const out = [];
  const caption = n.querySelector('caption');
  if (caption) out.push(`**${plain(caption)}**`, '');
  const cells = (tr) => tr.querySelectorAll('th, td').map((c) => squash(inline(c, base)).trim().replace(/\|/g, '\\|'));
  const head = n.querySelector('thead tr');
  const body = n.querySelectorAll('tbody tr');
  const h = head ? cells(head) : cells(body[0]);
  out.push(`| ${h.join(' | ')} |`, `| ${h.map(() => '---').join(' | ')} |`);
  for (const tr of head ? body : body.slice(1)) out.push(`| ${cells(tr).join(' | ')} |`);
  return out.join('\n');
}

/* The terminal render keeps one line per .ln span; any other <pre> is a
   shell command. Text comes out exactly, spaces included. */
function pre(n) {
  const lines = n.querySelectorAll('.ln');
  if (lines.length) return fence(lines.map((l) => l.text).join('\n'), 'text');
  return fence(n.text.replace(/^\n/, '').replace(/\n$/, ''), 'sh');
}

/* ---- assemble ---------------------------------------------------------- */

function frontmatter(root) {
  const title = plain(root.querySelector('title'));
  const desc = root.querySelector('meta[name="description"]')?.getAttribute('content');
  const canonical = root.querySelector('link[rel="canonical"]')?.getAttribute('href');
  // JSON strings are valid YAML double-quoted scalars: a placeholder's
  // brackets and colons cannot break the block.
  return [
    '---',
    `title: ${JSON.stringify(title)}`,
    desc && `description: ${JSON.stringify(desc)}`,
    canonical && `canonical: ${JSON.stringify(canonical)}`,
    '---',
  ].filter(Boolean).join('\n');
}

// node-html-parser keeps <pre> as raw text by default. The terminal render's
// lines are elements inside its <pre>, so <pre> is parsed like any element.
const PARSE = { blockTextElements: { script: true, noscript: true, style: true } };

for (const htmlPath of PAGES) {
  const root = parse(await readFile(htmlPath, 'utf8'), PARSE);
  const base = root.querySelector('link[rel="canonical"]').getAttribute('href');
  const body = blocks(root.querySelector('main'), base);
  const note = root.querySelector('footer .ai-note');
  if (note) body.push('---', squash(inline(note, base)).trim());
  const md = `${frontmatter(root)}\n\n${body.join('\n\n')}\n`.replace(/\n{3,}/g, '\n\n');
  const outPath = htmlPath.replace(/index\.html$/, 'index.md');
  await writeFile(outPath, md);
  console.log(`[build-md] ${outPath} (${Buffer.byteLength(md)} bytes)`);
}

// Copy-placeholder check: the built site and COPY.md must agree.
//
//   node scripts/check-copy.mjs        (after `pnpm run build`)
//
// Every [COPY · id · …] marker in dist/ (pages, Markdown editions, llms.txt,
// meta tags) must have a row in COPY.md with status "open". Every "open" row
// must still have its marker in dist/. A row whose copy was written is marked
// "filled", and its marker must be gone. So the table stays the true list of
// what a person still has to write (UX standard [02.1]).
//
// Exit codes: 0 in step, 1 out of step.

import { readdir, readFile } from 'node:fs/promises';
import { join } from 'node:path';

const ROOT = new URL('../', import.meta.url).pathname;
const DIST = join(ROOT, 'dist');
const MARKER = /\[COPY · ([a-z0-9-]+) · /g;

async function walk(dir) {
  const out = [];
  for (const e of await readdir(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) out.push(...(await walk(p)));
    else if (/\.(html|md|txt)$/.test(e.name)) out.push(p);
  }
  return out;
}

/* Markers on the built site: id -> files. */
const onSite = new Map();
for (const file of await walk(DIST)) {
  const body = await readFile(file, 'utf8');
  for (const [, id] of body.matchAll(MARKER)) {
    if (!onSite.has(id)) onSite.set(id, new Set());
    onSite.get(id).add(file.slice(DIST.length));
  }
}

/* Rows of the placeholder table in COPY.md: | id | page | … | status |.
   An id is kebab-case with at least one dash, which also skips the header
   row ("id") and the separator row ("----"). */
const table = new Map();
const copyMd = await readFile(join(ROOT, 'COPY.md'), 'utf8');
const section = copyMd.split(/^## /m).find((s) => s.startsWith('Placeholders')) ?? '';
for (const line of section.split('\n')) {
  const cells = line.split('|').slice(1, -1).map((c) => c.trim().replace(/`/g, ''));
  if (cells.length < 6 || !/^[a-z0-9]+(-[a-z0-9]+)+$/.test(cells[0])) continue;
  table.set(cells[0], cells[cells.length - 1]);
}

const problems = [];
for (const [id, files] of onSite) {
  const status = table.get(id);
  if (!status) problems.push(`${id}: on the site (${[...files].join(', ')}) but not in COPY.md`);
  else if (status !== 'open') problems.push(`${id}: marked "${status}" in COPY.md but still a placeholder on the site`);
}
for (const [id, status] of table) {
  if (status === 'open' && !onSite.has(id)) problems.push(`${id}: "open" in COPY.md but not on the site; mark it filled or remove the row`);
  if (status !== 'open' && status !== 'filled') problems.push(`${id}: unknown status "${status}" (use open or filled)`);
}

const open = [...table.values()].filter((s) => s === 'open').length;
console.log(`copy: ${onSite.size} placeholders on the site, ${open} open and ${table.size - open} filled in COPY.md`);
for (const p of problems) console.log(`  MISMATCH  ${p}`);
console.log(problems.length ? `\nFAIL: COPY.md and the site disagree` : '\nOK: COPY.md matches the site');
process.exit(problems.length ? 1 : 0);

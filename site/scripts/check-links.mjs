// Link checker for the BUILT site in dist/ (UX standard [19.2]: check every
// link before ship). Run it after `pnpm run build`:
//
//   node scripts/check-links.mjs
//
// Internal links and anchors are checked against the files in dist/ and
// FAIL the run when broken: a path with no file behind it, or a #fragment
// with no element of that id on the target page. External links are listed
// with the pages that use them, never fetched: the check stays offline and
// deterministic. Check those by hand before a release.
//
// Sources scanned: href/src in every HTML page; links in the Markdown
// editions and llms.txt; the Sitemap line in robots.txt; <loc> entries in the
// sitemaps; Link targets in _headers.
//
// An absolute URL on the site's own host (read from the canonical link in
// dist/index.html, so it follows astro.config.mjs `site`) is an internal
// link and is checked against dist/ too.
//
// Zero dependencies, like Personal-Site's checker: pnpm-workspace.yaml blocks
// install scripts, and a link check does not justify widening that policy.
//
// Exit codes: 0 clean, 1 broken internal link or anchor.

import { readdir, readFile } from 'node:fs/promises';
import { join, relative } from 'node:path';

const DIST = new URL('../dist/', import.meta.url).pathname;

async function walk(dir) {
  const out = [];
  for (const e of await readdir(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) out.push(...(await walk(p)));
    else out.push(p);
  }
  return out;
}

const rel = (file) => `/${relative(DIST, file)}`;

/* ---- collect ----------------------------------------------------------- */

const ATTR = /\s(?:href|src)\s*=\s*"([^"]*)"/gi;
const MD_LINK = /\]\(([^)\s]+)\)/g;
const LOC = /<loc>([^<]+)<\/loc>/g;
const SITEMAP_LINE = /^Sitemap:\s*(\S+)/gim;
const HEADER_LINK = /<(\/[^>]*)>/g;
const ID = /\sid="([^"]+)"/g;

const decode = (s) =>
  s.replace(/&amp;/g, '&').replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"').replace(/&#39;/g, "'");

function linksIn(file, body) {
  const pick = (re) => [...body.matchAll(re)].map((m) => decode(m[1]));
  if (file.endsWith('.html')) return pick(ATTR);
  if (file.endsWith('.md') || file.endsWith('llms.txt')) return pick(MD_LINK);
  if (file.endsWith('robots.txt')) return pick(SITEMAP_LINE);
  if (file.endsWith('.xml')) return pick(LOC);
  if (file.endsWith('_headers')) return body.split('\n').filter((l) => /^\s+Link:/.test(l)).flatMap((l) => [...l.matchAll(HEADER_LINK)].map((m) => m[1]));
  return [];
}

/* ---- resolve ----------------------------------------------------------- */

const files = await walk(DIST);
const present = new Set(files.map(rel));
const bodies = new Map();
for (const f of files) {
  if (/\.(html|md|txt|xml)$/.test(f) || f.endsWith('_headers')) bodies.set(rel(f), await readFile(f, 'utf8'));
}

const canonical = /<link rel="canonical" href="([^"]+)"/.exec(bodies.get('/index.html') ?? '');
if (!canonical) {
  console.log('FAIL: dist/index.html has no canonical link; build first.');
  process.exit(1);
}
const SITE = new URL(canonical[1]);

const idsCache = new Map();
function idsOf(path) {
  if (!idsCache.has(path)) {
    const body = bodies.get(path) ?? '';
    idsCache.set(path, new Set([...body.matchAll(ID)].map((m) => decode(m[1]))));
  }
  return idsCache.get(path);
}

/* The file a path serves, as Cloudflare Pages resolves it, and whether the
   request would first be redirected (a page URL without its slash). */
function serve(path) {
  if (present.has(path) && !path.endsWith('/')) return { file: path };
  if (path.endsWith('/') && present.has(`${path}index.html`)) return { file: `${path}index.html` };
  if (present.has(`${path}/index.html`)) return { file: `${path}/index.html`, redirect: `${path}/` };
  if (present.has(`${path}.html`)) return { file: `${path}.html` };
  return null;
}

const broken = [];
const redirects = [];
const external = new Map();
let checked = 0;

for (const [from, body] of bodies) {
  for (const raw of linksIn(from, body)) {
    const link = raw.trim();
    if (!link || link.startsWith('data:') || link.startsWith('mailto:') || link.startsWith('tel:')) continue;

    let url;
    try {
      url = new URL(link, new URL(from, SITE));
    } catch {
      broken.push({ from, link, why: 'not a valid URL' });
      continue;
    }
    if (url.origin !== SITE.origin) {
      if (!external.has(url.href)) external.set(url.href, new Set());
      external.get(url.href).add(from);
      continue;
    }

    checked++;
    const path = decodeURIComponent(url.pathname);
    const target = serve(path);
    if (!target) {
      broken.push({ from, link, why: `no file serves ${path}` });
      continue;
    }
    if (target.redirect) redirects.push({ from, link, to: target.redirect });
    const fragment = decodeURIComponent(url.hash.slice(1));
    if (fragment && target.file.endsWith('.html') && !idsOf(target.file).has(fragment)) {
      broken.push({ from, link, why: `no id="${fragment}" in ${target.file}` });
    }
  }
}

/* ---- report ------------------------------------------------------------ */

console.log(`site: ${SITE.origin}`);
console.log(`internal: ${checked} links and anchors checked, ${broken.length} broken`);
for (const b of broken) console.log(`  BROKEN    ${b.from}  ->  ${b.link}  (${b.why})`);
for (const r of redirects) console.log(`  REDIRECT  ${r.from}  ->  ${r.link}  (Pages answers with a redirect to ${r.to})`);

console.log(`external: ${external.size} URLs, listed, not fetched`);
for (const [url, from] of [...external].sort(([a], [b]) => a.localeCompare(b))) {
  console.log(`  ${url}\n      from ${[...from].sort().join(', ')}`);
}

console.log(broken.length ? `\nFAIL: ${broken.length} broken internal link(s) or anchor(s)` : '\nOK: no broken internal links or anchors');
process.exit(broken.length ? 1 : 0);

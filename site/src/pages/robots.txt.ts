import type { APIRoute } from 'astro';

/* robots.txt, owned by this repository. Built from `site` in astro.config.mjs
   so the placeholder domain lives in one place: when the real domain lands,
   the Sitemap line follows it.

   Content Signals (contentsignals.org): search, ai-input and ai-train all
   granted. This mirrors Personal-Site's stance (Oscar's decision for that
   site, 2026-08-25); nobody has ruled on it for this site yet. Keep it in
   step with the Content-Signal header in public/_headers and in
   functions/_middleware.js.

   Leave Cloudflare's managed robots.txt setting off: its prepended block
   asserts ai-train=no and would contradict this file in the same response. */
export const GET: APIRoute = ({ site }) => {
  const sitemap = new URL('/sitemap-index.xml', site).href;
  const body = [
    '# Crawlers and agents are welcome here. Content Signals per contentsignals.org.',
    'User-agent: *',
    'Content-Signal: search=yes, ai-input=yes, ai-train=yes',
    'Allow: /',
    '',
    `Sitemap: ${sitemap}`,
    '',
  ].join('\n');
  return new Response(body, { headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
};

// Cloudflare Pages middleware: content negotiation for agents.
//
// A page request carrying `Accept: text/markdown` is answered with the page's
// Markdown edition, which scripts/build-md.mjs writes at build time to
// <page>/index.md. Same contract as Cloudflare's Markdown for Agents
// (Pro-gated), self-hosted. The editions are also plain static files, so
// <page>/index.md can be fetched directly.
//
// Adapted from Personal-Site's functions/_middleware.js. Left out: its
// hireme-subdomain rewrite (no such host here) and its www-to-apex redirect.
// TODO(domain): add a canonical-host redirect once the domain exists.
export const onRequest = async (context) => {
  const url = new URL(context.request.url);
  const p = url.pathname;

  // Astro's hashed assets and any request for a file pass through unchanged.
  if (p.startsWith('/_') || /\.[a-z0-9]+$/i.test(p)) return context.next();

  const edition = `${p.endsWith('/') ? p : `${p}/`}index.md`;

  // Substring match on Accept: the same loose contract Cloudflare's converter
  // documents ("text/markdown as one of the options").
  const accept = context.request.headers.get('Accept') || '';
  if (accept.includes('text/markdown')) {
    const asset = await context.env.ASSETS.fetch(new URL(edition, url).toString());
    if (asset.ok) {
      const headers = new Headers(asset.headers);
      headers.set('Content-Type', 'text/markdown; charset=utf-8');
      headers.set('Vary', 'Accept');
      // Mirrors robots.txt and the site-wide header in public/_headers.
      headers.set('Content-Signal', 'search=yes, ai-input=yes, ai-train=yes');
      return new Response(asset.body, { status: 200, headers });
    }
    // No edition (an unknown path, or the 404): fall through to HTML.
  }

  // The page's own response, headers from _headers included: copied forward,
  // never rebuilt from scratch, so _headers still applies.
  const res = await context.next();
  const out = new Response(res.body, res);
  // Both formats share each page URL, so page responses vary by Accept.
  out.headers.set('Vary', 'Accept');

  // Advertise the Markdown edition of a real page. Set here, not in
  // _headers, because it differs per page. append() keeps the describedby
  // link that _headers adds.
  if (out.status === 200 && (out.headers.get('Content-Type') || '').includes('text/html')
      && edition !== '/404/index.md') {
    out.headers.append('Link', `<${edition}>; rel="alternate"; type="text/markdown"`);
  }
  return out;
};

// 官网 CSP：script-src 'self' 会拦住没有 src 的 script。
// 构建后运行。失败条件：内联 script、内联事件、跨域脚本、或不是 https://api.yhvpn.io 的绝对地址。
import { readdir, readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const API = 'https://api.yhvpn.io';
const here = path.dirname(fileURLToPath(import.meta.url));
const website = path.resolve(here, '..');
const repo = path.resolve(website, '../..');

export function stripComments(html) {
  return html.replace(/<!--[\s\S]*?-->/g, '');
}

function scriptSrc(attrs) {
  const m = /\bsrc\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>`]+))/i.exec(attrs);
  if (!m) return null;
  return (m[1] ?? m[2] ?? m[3] ?? '').trim();
}

export function checkHtml(filename, html) {
  const errors = [];
  const src = stripComments(html);
  const scriptRe = /<script\b([^>]*)>/gi;
  let m;
  while ((m = scriptRe.exec(src))) {
    const value = scriptSrc(m[1] || '');
    if (value == null) {
      errors.push(`${filename}: inline <script> without src`);
      continue;
    }
    if (!value || /^(?:javascript|data|https?|blob):/i.test(value) || value.startsWith('//')) {
      errors.push(`${filename}: script src is not a same-origin path: ${value || '(empty)'}`);
    }
  }
  if (/\son[a-z]+\s*=/i.test(src)) errors.push(`${filename}: inline event handler`);
  if (/javascript:/i.test(src)) errors.push(`${filename}: javascript: URL`);
  const metaRe = /<meta\b[^>]*>/gi;
  let meta;
  while ((meta = metaRe.exec(src))) {
    const tag = meta[0];
    if (!/\bname\s*=\s*["']yuhai-api["']/i.test(tag)) continue;
    const content = /\bcontent\s*=\s*(?:"([^"]*)"|'([^']*)')/i.exec(tag);
    const value = content ? (content[1] ?? content[2] ?? '').trim().replace(/\/$/, '') : '';
    if (value !== API) errors.push(`${filename}: yuhai-api meta must be ${API}`);
  }
  return errors;
}

function urlAllowed(url) {
  return url === API || url.startsWith(`${API}/`) || url.startsWith(`${API}?`) || url.startsWith(`${API}#`);
}

export function checkJs(filename, js) {
  const errors = [];
  const urlRe = /(?:https?:)?\/\/[A-Za-z0-9._~:/?#\[\]@!$&*+,;=%-]+/g;
  let m;
  while ((m = urlRe.exec(js))) {
    if (!urlAllowed(m[0])) errors.push(`${filename}: absolute URL is not ${API}: ${m[0]}`);
  }
  if (js.includes('yuhai-api') && /return\s+v\b/.test(js)) {
    errors.push(`${filename}: api host is taken from meta content`);
  }
  return errors;
}

function assert(cond, message) {
  if (!cond) throw new Error(message);
}

function selfTest() {
  const inline = checkHtml('fixture.html', '<script>alert(1)</script>');
  assert(inline.some((e) => e.includes('without src')), 'inline script should fail');
  assert(checkHtml('ok.html', '<script src="/assets/site.js" defer></script>').length === 0, 'same-origin src should pass');
  assert(checkHtml('astro.html', '<script is:inline src="/assets/web-register.js"></script>').length === 0, 'astro external script should pass');
  const cross = checkHtml('cdn.html', '<script src="https://cdn.example/a.js"></script>');
  assert(cross.some((e) => e.includes('same-origin')), 'cross-origin script should fail');
  assert(checkHtml('empty.html', '<script src=""></script>').some((e) => e.includes('same-origin')), 'empty src should fail');
  assert(checkHtml('click.html', '<button onclick="go()">x</button>').some((e) => e.includes('event handler')), 'onclick should fail');
  assert(checkHtml('meta.html', '<meta name="yuhai-api" content="https://api.yhvpn.io" />').length === 0, 'api meta should pass');
  assert(checkHtml('bad-meta.html', '<meta name="yuhai-api" content="https://evil.example" />').some((e) => e.includes('yuhai-api')), 'wrong api meta should fail');
  assert(checkJs('ok.js', "var API_ORIGIN = 'https://api.yhvpn.io'; fetch(API_ORIGIN + '/v1/auth/web/register')").length === 0, 'api origin should pass');
  assert(checkJs('evil.js', "fetch('https://evil.example/v1')").some((e) => e.includes('absolute URL')), 'other origin should fail');
  const returned = checkJs('base.js', "querySelector('meta[name=\"yuhai-api\"]'); var v = meta.content; return v;");
  assert(returned.some((e) => e.includes('meta content')), 'returning meta content should fail');
}

async function walk(dir, pred, out = []) {
  let entries;
  try {
    entries = await readdir(dir, { withFileTypes: true });
  } catch {
    return out;
  }
  for (const entry of entries) {
    if (entry.name === 'node_modules' || entry.name === '.astro') continue;
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) await walk(full, pred, out);
    else if (pred(full)) out.push(full);
  }
  return out;
}

function isHtml(file) {
  return file.endsWith('.html') || file.endsWith('.astro');
}

function isJs(file) {
  return file.endsWith('.js') && !file.endsWith(`${path.sep}scripts${path.sep}check-csp.mjs`);
}

async function exists(file) {
  try {
    await readFile(file);
    return true;
  } catch {
    return false;
  }
}

async function main() {
  selfTest();
  const errors = [];
  const dist = path.join(website, 'dist');
  if (!(await exists(path.join(dist, 'index.html')))) {
    errors.push('apps/website/dist is missing; run npm run build before check:csp');
  }

  const htmlFiles = [
    ...(await walk(path.join(website, 'src'), isHtml)),
    ...(await walk(path.join(website, 'public'), isHtml)),
    ...(await walk(dist, isHtml)),
    ...(await walk(path.join(repo, 'deploy', 'site-patch'), isHtml)),
  ];
  const jsFiles = [
    ...(await walk(path.join(website, 'public'), isJs)),
    ...(await walk(dist, isJs)),
    ...(await walk(path.join(repo, 'deploy', 'site-patch'), isJs)),
  ];

  for (const file of htmlFiles) {
    const html = await readFile(file, 'utf8');
    errors.push(...checkHtml(path.relative(repo, file), html));
    const root = file.startsWith(dist) ? dist : file.startsWith(path.join(website, 'src')) || file.startsWith(path.join(website, 'public')) ? path.join(website, 'public') : null;
    if (!root) continue;
    const stripped = stripComments(html);
    const scriptRe = /<script\b([^>]*)>/gi;
    let m;
    while ((m = scriptRe.exec(stripped))) {
      const value = scriptSrc(m[1] || '');
      if (!value || !value.startsWith('/')) continue;
      const clean = value.split(/[?#]/)[0];
      const target = path.join(root, clean);
      if (!(await exists(target))) errors.push(`${path.relative(repo, file)}: missing script file ${value}`);
    }
  }
  for (const file of jsFiles) {
    errors.push(...checkJs(path.relative(repo, file), await readFile(file, 'utf8')));
  }

  if (errors.length) {
    console.error(errors.join('\n'));
    process.exit(1);
  }
  console.log(`csp ok: ${htmlFiles.length} html, ${jsFiles.length} js`);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});

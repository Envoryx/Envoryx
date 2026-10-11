// Builds the Envoryx logo files from one ASCII grid: the brand frame at the end of the
// Foundry film (web/src/features/foundry/film.js, finale), so the logo looks the same
// wherever it appears. Run it after changing the grid:
//
//   node scripts/logo/build.mjs
//
// It writes the SVG the web UI inlines, the favicon, and PNGs for the README,
// the GitHub avatar, the Unraid template, the touch icon and small icons (rendered with the
// Playwright Chromium of web/node_modules).
import { writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '../..');

// The mark, the 5x5 letters of the wordmark and the tagline, as film.js draws them.
const FONT = {
	E: ['11111', '10000', '11110', '10000', '11111'], N: ['10001', '11001', '10101', '10011', '10001'],
	V: ['10001', '10001', '10001', '01010', '00100'], O: ['01110', '10001', '10001', '10001', '01110'],
	R: ['11110', '10001', '11110', '10100', '10010'], Y: ['10001', '01010', '00100', '00100', '00100'],
	X: ['10001', '01010', '00100', '01010', '10001'],
};
const TAGLINE = 'B U I L D   _   D E E P E R';

/** The mark: 45 x 17 cells of [char, 'a' (accent) | 'f' (foreground)]. */
function mark() {
	const cells = new Map();
	for (let y = 0; y < 17; y++) for (let x = 0; x < 45; x++) {
		const left = Math.abs(x - (Math.abs(y - 8) * 1.15 + 1)) < 2;
		const right = Math.abs(x - (43 - Math.abs(y - 8) * 1.15)) < 2;
		const top = y <= 2 && x >= 15 && x <= 29;
		const mid = y >= 7 && y <= 9 && x >= 15 && x <= 23;
		const bottom = y >= 14 && x >= 15 && x <= 29;
		if (left || right || top || mid || bottom) cells.set(`${x},${y}`, [left ? '/' : right ? '\\' : bottom ? '=' : '#', left || right ? 'a' : 'f']);
	}
	return { width: 45, height: 17, cells };
}

/** Mark, wordmark and tagline: 83 x 29 cells. */
function full() {
	const width = 83, cells = new Map();
	const m = mark(), mx = Math.round((width - m.width) / 2);
	for (const [k, v] of m.cells) { const [x, y] = k.split(',').map(Number); cells.set(`${x + mx},${y}`, v); }
	let offset = 0;
	[...'ENVORYX'].forEach((letter, i) => {
		FONT[letter].forEach((row, j) => [...row].forEach((bit, k) => {
			if (bit === '1') for (const d of [0, 1]) cells.set(`${offset + k * 2 + d},${20 + j}`, ['#', i < 3 ? 'a' : 'f']);
		}));
		offset += 12;
	});
	const tx = Math.round((width - TAGLINE.length) / 2);
	[...TAGLINE].forEach((c, i) => { if (c !== ' ') cells.set(`${tx + i},28`, [c, c === '_' ? 'a' : 'f']); });
	return { width, height: 29, cells };
}

// One cell is 0.6 em wide, like every monospace font; the rows keep the film's 8:13 cell.
const FS = 10, CW = 6, LH = 9.75;
const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

/** The grid as SVG text: one <text> per run of characters, placed and sized to the cell grid. */
function svg(grid, { pad = 0, colors, background, size } = {}) {
	const w = grid.width * CW + pad * 2, h = grid.height * LH + pad * 2;
	const style = colors
		? `.a{fill:${colors.a}}.f{fill:${colors.f}}`
		: '.a{fill:var(--logo-accent,#2ffabe)}.f{fill:var(--logo-fg,currentColor)}';
	const rows = [];
	for (let y = 0; y < grid.height; y++) {
		let x = 0;
		while (x < grid.width) {
			const [c, cls] = grid.cells.get(`${x},${y}`) ?? [' ', null];
			if (c === ' ') { x++; continue; }
			let run = '', end = x;
			while (end < grid.width) {
				const [c2, cls2] = grid.cells.get(`${end},${y}`) ?? [' ', null];
				if (c2 === ' ' || cls2 !== cls) break;
				run += c2; end++;
			}
			rows.push(`<text class="${cls}" x="${pad + x * CW}" y="${(pad + (y + 0.78) * LH).toFixed(2)}" textLength="${run.length * CW}" lengthAdjust="spacingAndGlyphs">${esc(run)}</text>`);
			x = end;
		}
	}
	const dims = size ? ` width="${size[0]}" height="${size[1]}"` : '';
	const bg = background ? `<rect width="100%" height="100%" ${background}/>` : '';
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${w} ${h}"${dims} font-family="ui-monospace,SFMono-Regular,Menlo,Consolas,'DejaVu Sans Mono','Liberation Mono',monospace" font-size="${FS}">
<title>Envoryx - build deeper</title>
<style>${style}</style>${bg}
${rows.join('\n')}
</svg>
`;
}

const write = (rel, data) => { writeFileSync(join(root, rel), data); console.log('wrote', rel); };
// Two colours and nothing else: the accent (mint unless the UI sets --logo-accent) and the
// white, always on the dark ground.
const DARK = { a: '#2ffabe', f: '#f0f1f2' }, BG = '#06070a';

write('web/src/layout/envoryx-logo.svg', svg(full(), { pad: 28, colors: { a: `var(--logo-accent,${DARK.a})`, f: DARK.f }, background: `rx="14" fill="${BG}"` }));
// At favicon size the picture would blur: the mark's three characters instead.
write('web/public/favicon.svg', `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="${BG}"/><text x="32" y="42" text-anchor="middle" font-family="ui-monospace,SFMono-Regular,Menlo,Consolas,'DejaVu Sans Mono',monospace" font-size="28" font-weight="700" letter-spacing="-1"><tspan fill="${DARK.a}">&lt;</tspan><tspan fill="${DARK.f}">E</tspan><tspan fill="${DARK.a}">&gt;</tspan></text></svg>\n`);

// PNGs, rendered by Chromium.
const { chromium } = await import(join(root, 'web/node_modules/playwright/index.mjs'));
const browser = await chromium.launch();
const page = await browser.newPage();
async function png(rel, markup, width, height, transparent = false) {
	await page.setViewportSize({ width, height });
	await page.setContent(`<!doctype html><style>html,body{margin:0;background:transparent}svg{display:block;width:${width}px;height:${height}px}</style>${markup}`);
	await page.screenshot({ path: join(root, rel), omitBackground: transparent });
	console.log('wrote', rel);
}
const square = (pad) => { const m = mark(); const w = m.width * CW, h = m.height * LH, s = w + pad * 2; return { m, s, dy: (s - h) / 2 - pad }; };
function squareSvg(pad, background) {
	const { m, s, dy } = square(pad);
	return svg(m, { colors: DARK }).replace(/viewBox="[^"]*"/, `viewBox="${-pad} ${-pad - dy} ${s} ${s}"`).replace('<style>', `<rect x="${-pad}" y="${-pad - dy}" width="${s}" height="${s}" ${background}/><style>`);
}
await png('docs/assets/envoryx-logo.png', svg(full(), { pad: 40, colors: DARK, background: `fill="${BG}"` }), 1440, Math.round(1440 * (29 * LH + 80) / (83 * CW + 80)));
await png('docs/assets/envoryx-avatar.png', squareSvg(40, `fill="${BG}"`), 1024, 1024);
await png('deploy/unraid/envoryx.png', squareSvg(30, `rx="60" fill="${BG}"`), 512, 512, true);
// Small icons show the three characters, like the favicon.
const icon = (radius) => `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="${radius}" fill="${BG}"/><text x="32" y="42" text-anchor="middle" font-family="ui-monospace,SFMono-Regular,Menlo,Consolas,'DejaVu Sans Mono',monospace" font-size="28" font-weight="700" letter-spacing="-1"><tspan fill="${DARK.a}">&lt;</tspan><tspan fill="${DARK.f}">E</tspan><tspan fill="${DARK.a}">&gt;</tspan></text></svg>`;
await png('web/public/apple-touch-icon.png', icon(0), 180, 180);
await png('docs/assets/envoryx-icon.png', icon(14), 256, 256, true);
await browser.close();

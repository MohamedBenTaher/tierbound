// Records the public-page clips of the tierbound demo (docs/demo-script.md)
// and, optionally, stitches them with the owner's Cursor recording.
//
//   node record.mjs before <issue>       the issue before approval
//   node record.mjs after  <issue>       waits for the one bot comment, then shows it
//   node record.mjs gate                 PR #2 checks with the red eval-gate
//   node record.mjs compose <cursor.mov> before + cursor + after + gate, captions burned in
//
// Public pages only: no sign-in, no token, nothing is posted or approved.
// Comment polling uses the unauthenticated REST API with ETags (a 304 does
// not count against the 60/hour limit).
import { chromium } from "playwright";
import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const REPO = process.env.DEMO_REPO ?? "MohamedBenTaher/tierbound";
const BOT = process.env.DEMO_BOT ?? "tierbound-mbt[bot]";
const GATE_PR = process.env.DEMO_GATE_PR ?? "2";
const OUT = resolve(process.env.DEMO_OUT ?? join(ROOT, "demo-out"));
const THEME = process.env.DEMO_THEME === "dark" ? "dark" : "light";
const WAIT_SECONDS = Number(process.env.DEMO_WAIT_SECONDS ?? 300);
const SIZE = { width: 1280, height: 720 };
const FPS = 30;

const log = (msg) => console.log(`▸ ${msg}`);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const hasFfmpeg = () => {
  try { execFileSync("ffmpeg", ["-version"], { stdio: "ignore" }); return true; } catch { return false; }
};

function ensureOut() {
  mkdirSync(OUT, { recursive: true });
  // Self-ignoring output folder: no change to the repo's .gitignore needed.
  const ignore = join(OUT, ".gitignore");
  if (!existsSync(ignore)) writeFileSync(ignore, "*\n");
}

// Records one clip. `scene(page, markStart)` drives the page; markStart() is
// called once the page is presentable so the blank loading frames are trimmed.
async function record(name, scene) {
  ensureOut();
  const raw = mkdtempSync(join(tmpdir(), "tierbound-demo-"));
  const browser = await chromium.launch();
  const context = await browser.newContext({
    viewport: SIZE, deviceScaleFactor: 1, colorScheme: THEME,
    recordVideo: { dir: raw, size: SIZE },
  });
  const page = await context.newPage();
  const t0 = Date.now();
  let trim = 0;
  let ok = false;
  try {
    await scene(page, () => { trim = (Date.now() - t0) / 1000; });
    ok = true;
  } finally {
    const video = page.video();
    await context.close(); // finalizes the recording
    const webm = await video.path();
    await browser.close();
    if (!ok) {
      rmSync(raw, { recursive: true, force: true });
    } else if (hasFfmpeg()) {
      const mp4 = join(OUT, `${name}.mp4`);
      execFileSync("ffmpeg", ["-y", "-loglevel", "error", "-ss", trim.toFixed(2), "-i", webm,
        "-vf", `fps=${FPS},format=yuv420p`, "-c:v", "libx264", "-crf", "18", "-preset", "medium", "-an", mp4]);
      log(`saved ${mp4} (trimmed ${trim.toFixed(1)} s of loading)`);
    } else {
      const out = join(OUT, `${name}.webm`);
      execFileSync("cp", [webm, out]);
      log(`saved ${out} (no ffmpeg: untrimmed webm)`);
    }
    if (ok) rmSync(raw, { recursive: true, force: true });
  }
}

async function smoothScrollBy(page, dy, ms) {
  await page.evaluate(async ([dy, ms]) => {
    const start = window.scrollY, t0 = performance.now();
    await new Promise((done) => {
      const step = (t) => {
        const k = Math.min(1, (t - t0) / ms), ease = k < 0.5 ? 2 * k * k : 1 - (-2 * k + 2) ** 2 / 2;
        window.scrollTo(0, start + dy * ease);
        k < 1 ? requestAnimationFrame(step) : done();
      };
      requestAnimationFrame(step);
    });
  }, [dy, ms]);
}

async function settle(page, url) {
  await page.goto(url, { waitUntil: "load" });
  await page.waitForTimeout(1500); // GitHub hydrates after load
}

const issueUrl = (n) => `https://github.com/${REPO}/issues/${n}`;

async function botComments(issue, state) {
  const headers = { Accept: "application/vnd.github+json", "User-Agent": "tierbound-demo-record" };
  if (state.etag) headers["If-None-Match"] = state.etag;
  const res = await fetch(`https://api.github.com/repos/${REPO}/issues/${issue}/comments?per_page=100`, { headers });
  if (res.status === 304) return state.last;
  if (!res.ok) throw new Error(`GitHub API ${res.status} for issue #${issue}`);
  state.etag = res.headers.get("etag");
  state.last = (await res.json()).filter((c) => c.user?.login === BOT);
  return state.last;
}

async function before(issue) {
  await record(`before-${issue}`, async (page, markStart) => {
    await settle(page, issueUrl(issue));
    markStart();
    await page.waitForTimeout(2500);
    await smoothScrollBy(page, 120, 1200);
    await page.waitForTimeout(2500);
  });
}

async function after(issue) {
  const state = {};
  const started = Date.now();
  log(`waiting for a ${BOT} comment on #${issue} (approve it in Cursor now; timeout ${WAIT_SECONDS} s)`);
  let comments = await botComments(issue, state);
  while (comments.length === 0) {
    if (Date.now() - started > WAIT_SECONDS * 1000) throw new Error(`no ${BOT} comment on #${issue} after ${WAIT_SECONDS} s`);
    await sleep(2000);
    comments = await botComments(issue, state);
  }
  log(`comment seen after ${((Date.now() - started) / 1000).toFixed(1)} s`);
  // The point of the demo is "exactly once": re-check after a pause so a
  // duplicate post would show up here instead of on camera.
  await sleep(4000);
  comments = await botComments(issue, {});
  if (comments.length !== 1) throw new Error(`expected exactly one ${BOT} comment, found ${comments.length}`);
  const id = comments[0].id;
  log(`exactly one bot comment: ${comments[0].html_url}`);
  await record(`after-${issue}`, async (page, markStart) => {
    await settle(page, issueUrl(issue));
    markStart();
    await page.waitForTimeout(1500);
    const target = page.locator(`[id="issuecomment-${id}"]`);
    await target.waitFor({ timeout: 15000 });
    const dy = await target.evaluate((el) => el.getBoundingClientRect().top - 120);
    await smoothScrollBy(page, dy, 1600);
    // A soft outline so the eye lands on the one comment.
    await target.evaluate((el) => {
      el.style.transition = "box-shadow .5s";
      el.style.borderRadius = "8px";
      el.style.boxShadow = "0 0 0 3px rgba(20,184,166,.85)";
    });
    await page.waitForTimeout(3500);
  });
}

// The eval-gate run linked from the PR's checks tab, e.g. /actions/runs/123.
async function evalGateRunPath(page) {
  await settle(page, `https://github.com/${REPO}/pull/${GATE_PR}/checks`);
  const href = await page.locator("a", { hasText: "eval-gate" }).first().getAttribute("href");
  const run = String(href).match(/\/actions\/runs\/\d+/);
  if (!run) throw new Error(`no eval-gate run linked from PR #${GATE_PR}`);
  return run[0];
}

async function scrollToText(page, text, offset) {
  const el = page.getByText(text, { exact: true }).first();
  if (!(await el.count())) return;
  const dy = await el.evaluate((node, off) => node.getBoundingClientRect().top - off, offset);
  if (dy > 0) await smoothScrollBy(page, dy, 1500);
}

async function gate() {
  await record(`gate-pr${GATE_PR}`, async (page, markStart) => {
    const run = await evalGateRunPath(page); // not filmed: trimmed
    // The run summary shows lint ✓ / eval-gate ✗ and, logged out too, the
    // failing annotation.
    await settle(page, `https://github.com/${REPO}${run}`);
    markStart();
    await page.waitForTimeout(2800);
    await scrollToText(page, "Annotations", 260);
    await page.waitForTimeout(3500);
  });
}

// --- compose: before + cursor + after + gate with captions ------------------

// The shot table in docs/demo-script.md as row objects keyed by lowercased
// header ("clip", "caption", ...).
function shotRows(md) {
  const lines = md.split("\n").filter((l) => l.startsWith("|") && !/^\|\s*-/.test(l));
  const [head, ...rows] = lines.map((l) => l.split("|").map((c) => c.trim()));
  const keys = head.map((h) => h.toLowerCase());
  if (!keys.includes("clip") || !keys.includes("caption")) {
    throw new Error("docs/demo-script.md shot table needs Clip and Caption columns");
  }
  return rows.map((cells) => Object.fromEntries(keys.map((k, i) => [k, cells[i] ?? ""])));
}

const plain = (cell) => cell.replace(/`|\*\*/g, "");

// Every row whose Clip column names a segment contributes its caption, in order.
function captionsBySegment() {
  const rows = shotRows(readFileSync(join(ROOT, "docs/demo-script.md"), "utf8"));
  const out = {};
  for (const row of rows.filter((r) => r.clip && r.caption)) {
    (out[plain(row.clip)] ||= []).push(plain(row.caption));
  }
  return out;
}

async function renderCaptions(texts, dir) {
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: SIZE });
  const files = [];
  for (const [i, text] of texts.entries()) {
    await page.setContent(`<html><body style="margin:0;background:transparent;width:${SIZE.width}px;height:${SIZE.height}px;
      display:flex;align-items:flex-end;justify-content:center;font:600 30px/1.3 -apple-system,'SF Pro Display','Helvetica Neue',sans-serif">
      <div style="margin-bottom:44px;max-width:1080px;padding:14px 26px;border-radius:14px;background:rgba(13,17,23,.82);color:#fff;text-align:center">
      </div></body></html>`);
    await page.locator("div").evaluate((el, t) => { el.textContent = t; }, text);
    const file = join(dir, `caption-${i}.png`);
    await page.screenshot({ path: file, omitBackground: true });
    files.push(file);
  }
  await browser.close();
  return files;
}

const duration = (file) => Number(execFileSync("ffprobe", ["-v", "error", "-show_entries", "format=duration",
  "-of", "default=nw=1:nk=1", file]).toString().trim());

// Newest demo-out/<prefix>*.mp4.
function latestClip(prefix) {
  const hits = execFileSync("ls", ["-t", OUT]).toString().split("\n").filter((f) => f.startsWith(prefix) && f.endsWith(".mp4"));
  if (!hits.length) throw new Error(`no ${prefix}*.mp4 in ${OUT}; record it first`);
  return join(OUT, hits[0]);
}

// Spreads each segment's captions evenly over that segment's time span.
function captionTimeline(segments, captions) {
  const timeline = [];
  let t = 0;
  for (const s of segments) {
    const texts = captions[s.name] ?? [];
    const slot = s.duration / Math.max(texts.length, 1);
    texts.forEach((text, i) => timeline.push({ text, from: t + slot * i, to: t + slot * (i + 1) }));
    t += s.duration;
  }
  return { timeline, total: t };
}

// Normalize every segment to 1280x720@30 (letterboxed), concat, then overlay
// caption i (input segments.length + i) during its window.
function filterGraph(segments, timeline) {
  const norm = segments.map((_, i) =>
    `[${i}:v]scale=${SIZE.width}:${SIZE.height}:force_original_aspect_ratio=decrease,pad=${SIZE.width}:${SIZE.height}:(ow-iw)/2:(oh-ih)/2:color=0x0d1117,setsar=1,fps=${FPS},format=yuv420p[v${i}]`);
  const concat = segments.map((_, i) => `[v${i}]`).join("") + `concat=n=${segments.length}:v=1:a=0[c-1]`;
  const overlays = timeline.map((c, i) =>
    `[c${i - 1}][${segments.length + i}:v]overlay=0:0:enable='between(t,${c.from.toFixed(2)},${c.to.toFixed(2)})'[c${i}]`);
  return { graph: [...norm, concat, ...overlays].join(";"), out: `[c${timeline.length - 1}]` };
}

async function compose(cursorRecording) {
  if (!hasFfmpeg()) throw new Error("compose needs ffmpeg (brew install ffmpeg)");
  if (!existsSync(String(cursorRecording))) throw new Error("usage: compose <cursor-recording.mov>");
  const segments = [
    { name: "before", file: latestClip("before-") },
    { name: "cursor", file: resolve(cursorRecording) },
    { name: "after", file: latestClip("after-") },
    { name: "gate", file: latestClip("gate-") },
  ].map((s) => ({ ...s, duration: duration(s.file) }));
  const { timeline, total } = captionTimeline(segments, captionsBySegment());
  const work = mkdtempSync(join(tmpdir(), "tierbound-compose-"));
  const pngs = await renderCaptions(timeline.map((c) => c.text), work);
  const { graph, out: last } = filterGraph(segments, timeline);
  const inputs = [...segments.map((s) => s.file), ...pngs].flatMap((f) => ["-i", f]);
  ensureOut();
  const out = join(OUT, "tierbound-demo.mp4");
  execFileSync("ffmpeg", ["-y", "-loglevel", "error", ...inputs, "-filter_complex", graph, "-map", last,
    "-c:v", "libx264", "-crf", "18", "-preset", "medium", "-pix_fmt", "yuv420p", "-movflags", "+faststart", out], { stdio: "inherit" });
  rmSync(work, { recursive: true, force: true });
  log(`saved ${out} (${total.toFixed(1)} s, ${timeline.length} captions)`);
  if (total > 50) log(`note: ${total.toFixed(0)} s is over the 45 s target; trim the Cursor recording`);
}

const [cmd, arg] = process.argv.slice(2);
const issueArg = () => {
  if (!/^\d+$/.test(arg ?? "")) throw new Error(`usage: ${cmd} <issue-number>`);
  return arg;
};
try {
  if (cmd === "before") await before(issueArg());
  else if (cmd === "after") await after(issueArg());
  else if (cmd === "gate") await gate();
  else if (cmd === "compose") await compose(arg);
  else throw new Error("usage: before <issue> | after <issue> | gate | compose <cursor-recording.mov>");
} catch (err) {
  console.error(`✗ ${err.message}`);
  process.exit(1);
}

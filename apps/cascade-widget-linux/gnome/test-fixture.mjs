/**
 * test-fixture.mjs
 *
 * Node.js ESM smoke-test for the parseCache() logic used in extension.js.
 * GLib is unavailable in Node, so the function is re-implemented here using
 * fs.readFileSync. The parsing contract is identical; only the I/O primitive
 * differs.
 *
 * Run: node test-fixture.mjs
 * Exit 0 on success, non-zero on any assertion failure.
 */
import {readFileSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import path from 'node:path';

// Resolve the fixture path relative to this file so the test is
// runnable from any working directory.
const __dirname = path.dirname(fileURLToPath(import.meta.url));
const fixturePath = path.resolve(__dirname, '../fixtures/cache-v1.json');

// ---------------------------------------------------------------------------
// Node-environment implementation of parseCache (mirrors extension.js logic).
// ---------------------------------------------------------------------------

/** Default returned on missing file or parse error. */
const DEFAULTS = {
  quota_pcts: {t0: 0, t1: 0, t2: 0, t3: 0},
  inbox_unread: 0,
  active_agents: 0,
  cache_age_s: 0,
};

function parseCache(filePath) {
  try {
    const text = readFileSync(filePath, 'utf8');
    const d = JSON.parse(text);
    const q = d.quota;
    return {
      quota_pcts: {
        t0: q.t0.pct_used,
        t1: q.t1.pct_used,
        t2: q.t2.pct_used,
        t3: q.t3.pct_used,
      },
      inbox_unread: d.inbox.unread,
      active_agents: d.agents.active,
      cache_age_s: Math.round((Date.now() - Date.parse(d.updated_at)) / 1000),
    };
  } catch (_err) {
    return {...DEFAULTS};
  }
}

// ---------------------------------------------------------------------------
// Assertion helper — throws on failure so the process exits non-zero.
// ---------------------------------------------------------------------------
function assert(condition, message) {
  if (!condition) {
    throw new Error(`ASSERTION FAILED: ${message}`);
  }
}

// ---------------------------------------------------------------------------
// Test 1: parse the fixture file.
// ---------------------------------------------------------------------------
const result = parseCache(fixturePath);

assert(result.quota_pcts.t0 === 42, `quota_pcts.t0 expected 42, got ${result.quota_pcts.t0}`);
assert(result.quota_pcts.t1 === 15, `quota_pcts.t1 expected 15, got ${result.quota_pcts.t1}`);
assert(result.quota_pcts.t2 === 67, `quota_pcts.t2 expected 67, got ${result.quota_pcts.t2}`);
assert(result.quota_pcts.t3 === 8,  `quota_pcts.t3 expected 8, got ${result.quota_pcts.t3}`);
assert(result.inbox_unread === 3,    `inbox_unread expected 3, got ${result.inbox_unread}`);
assert(result.active_agents === 2,   `active_agents expected 2, got ${result.active_agents}`);
assert(typeof result.cache_age_s === 'number' && result.cache_age_s >= 0,
  `cache_age_s expected non-negative number, got ${result.cache_age_s}`);

// ---------------------------------------------------------------------------
// Test 2: missing file → all-zeros defaults (no throw).
// ---------------------------------------------------------------------------
const missing = parseCache('/nonexistent/path/cache.json');

assert(missing.quota_pcts.t0 === 0,  `missing: quota_pcts.t0 expected 0, got ${missing.quota_pcts.t0}`);
assert(missing.quota_pcts.t1 === 0,  `missing: quota_pcts.t1 expected 0, got ${missing.quota_pcts.t1}`);
assert(missing.quota_pcts.t2 === 0,  `missing: quota_pcts.t2 expected 0, got ${missing.quota_pcts.t2}`);
assert(missing.quota_pcts.t3 === 0,  `missing: quota_pcts.t3 expected 0, got ${missing.quota_pcts.t3}`);
assert(missing.inbox_unread === 0,   `missing: inbox_unread expected 0, got ${missing.inbox_unread}`);
assert(missing.active_agents === 0,  `missing: active_agents expected 0, got ${missing.active_agents}`);
assert(missing.cache_age_s === 0,    `missing: cache_age_s expected 0, got ${missing.cache_age_s}`);

// ---------------------------------------------------------------------------
// Test 3: malformed JSON → defaults (no throw).
// ---------------------------------------------------------------------------
import {writeFileSync, unlinkSync} from 'node:fs';
import os from 'node:os';

const tmpPath = path.join(os.tmpdir(), `cascade-test-malformed-${Date.now()}.json`);
writeFileSync(tmpPath, '{not valid json', 'utf8');
const malformed = parseCache(tmpPath);
unlinkSync(tmpPath);

assert(malformed.quota_pcts.t0 === 0, `malformed: quota_pcts.t0 expected 0, got ${malformed.quota_pcts.t0}`);
assert(malformed.inbox_unread === 0,  `malformed: inbox_unread expected 0, got ${malformed.inbox_unread}`);

// ---------------------------------------------------------------------------
// All assertions passed.
// ---------------------------------------------------------------------------
console.log('parseCache: all assertions passed');

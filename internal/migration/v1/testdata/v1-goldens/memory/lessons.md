# Lessons Learned

Gotchas and mistakes discovered during development. Read before working on related areas.

---

## 2026-05-18: ESM/CJS exports map order matters

**Context:** Consumer using `require()` was getting the ESM bundle instead of CJS.

**Root cause:** The exports map had `"import"` before `"require"`. Some bundlers and
Node versions process the first matching condition, and bundlers running in CJS mode
matched `"import"` first due to a bug in their resolver.

**Fix:** Put `"types"` first, then `"import"`, then `"require"`. The flat exports shape
(not nested `import.types`/`require.types`) resolves correctly in all tested toolchains.

**Correct shape:**
```json
".": {
  "types": "./dist/index.d.ts",
  "import": "./dist/index.mjs",
  "require": "./dist/index.cjs"
}
```

---

## 2026-05-02: `pnpm pack --dry-run` vs `npm pack --dry-run`

**Context:** CI was passing but the published package was missing `dist/` files.

**Root cause:** `pnpm pack --dry-run` showed the right files, but the actual `npm publish`
ran a fresh build that didn't output to the path listed in `files`. The `prepack` script
was not set.

**Fix:** Add `"prepack": "pnpm run build"` to package.json scripts. This ensures
`dist/` is always fresh before packing, regardless of local build state.

---

## 2026-04-30: TypeScript declaration maps vs .d.ts.map

**Context:** IDE "go to source" was jumping to dist/index.d.ts instead of src/index.ts.

**Root cause:** tsup was generating `.d.ts` files but not `.d.ts.map` source maps.

**Fix:** Add `dts: { resolve: true }` in tsup.config.ts. This generates both `.d.ts`
and `.d.ts.map`, letting IDEs navigate to the TypeScript source.

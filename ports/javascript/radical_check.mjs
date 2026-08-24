import { readdirSync, readFileSync } from "node:fs";
import { join, dirname, basename } from "node:path";
import { fileURLToPath } from "node:url";
import { process as outfixProcess } from "./outfix.mjs";

const base = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "testdata", "radical");
let files;
try {
  files = readdirSync(base).filter((f) => f.endsWith(".in")).sort();
} catch {
  files = [];
}
if (!files.length) {
  console.log("no corpus");
  process.exit(0);
}

const opts = { stripReasoning: true, repairJson: true, repairXml: true };
const mismatch = [];
for (const f of files) {
  const p = join(base, f);
  const raw = readFileSync(p, "utf8");
  const want = readFileSync(p.replace(/\.in$/, ".go.out"), "utf8");
  const got = outfixProcess(raw, opts).output;
  if (got !== want) mismatch.push([f, raw, want, got]);
}

console.log(`javascript: ${files.length - mismatch.length}/${files.length} identical to Go`);
for (const [f, raw, want, got] of mismatch.slice(0, 10)) {
  console.log("DIFF", basename(f));
  console.log("  in :", JSON.stringify(raw.slice(0, 100)));
  console.log("  go :", JSON.stringify(want.slice(0, 100)));
  console.log("  js :", JSON.stringify(got.slice(0, 100)));
}
if (mismatch.length) process.exit(1);

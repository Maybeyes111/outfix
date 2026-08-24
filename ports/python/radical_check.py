import glob
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__)))
from outfix.core import Options, process  # noqa: E402

base = os.path.join(os.path.dirname(__file__), "..", "..", "testdata", "radical")
files = sorted(glob.glob(os.path.join(base, "*.in")))
if not files:
    print("no corpus")
    sys.exit(0)

opts = Options()
mismatch = []
for f in files:
    raw = open(f, encoding="utf-8").read()
    want = open(f[:-3] + ".go.out", encoding="utf-8").read()
    got = process(raw, opts).output
    if got != want:
        mismatch.append((f, raw, want, got))

print(f"python: {len(files) - len(mismatch)}/{len(files)} identical to Go")
for f, raw, want, got in mismatch[:10]:
    print("DIFF", os.path.basename(f))
    print("  in :", repr(raw[:100]))
    print("  go :", repr(want[:100]))
    print("  py :", repr(got[:100]))
sys.exit(1 if mismatch else 0)

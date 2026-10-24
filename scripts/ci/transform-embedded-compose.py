#!/usr/bin/env python3
"""Transform the production Compose document for the embedded-update fixture."""
import pathlib
import sys

src, dst, volume, image = sys.argv[1:5]
source = pathlib.Path(src).read_text()
# Keep unrelated text/comments intact while requiring exact service/image targets.
lines = source.splitlines(keepends=True)
service = None
patched = 0
out: list[str] = []
for line in lines:
    if line.startswith("  ") and not line.startswith("    ") and line.rstrip().endswith(":"):
        service = line.strip()[:-1]
    if service == "app" and line.startswith("    image:"):
        expected_image = "${BANSOKO_BUNDLE_IMAGE_REPOSITORY:-wangkayu/bansoko}@${BANSOKO_BUNDLE_IMAGE_DIGEST:?BANSOKO_BUNDLE_IMAGE_DIGEST (sha256:...) must be injected by the builder}"
        if line.rstrip("\n") != f"    image: {expected_image}":
            raise SystemExit(f"unexpected {service}.image template: {line.rstrip()}")
        out.append(f"    image: {image}\n")
        patched += 1
    else:
        out.append(line)
if patched != 1:
    raise SystemExit(f"expected exactly one app image field, patched {patched}")
text = "".join(out)
text = text.replace("source: updates", f"source: {volume}")
text = text.replace("  updates:\n", f"  {volume}:\n    external: true\n")
text = text.replace("BANSOKO_ENVIRONMENT: production", "BANSOKO_ENVIRONMENT: test")
text = "".join(line for line in text.splitlines(keepends=True) if not line.lstrip().startswith("container_name:"))
pathlib.Path(dst).write_text(text)

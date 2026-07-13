#!/bin/sh
# The local mirror of the gate action: run before pushing anything meant
# for the fleet. set -eu plus explicit checks, no exit codes laundered
# through pipes. Packages run in series (-p 1) so a memory-pressed box
# does not stall parallel test binaries and read as a deadlock.
set -eu
cd "$(dirname "$0")/.."
fmt=$(gofmt -l .)
if [ -n "$fmt" ]; then
  echo "gofmt wants these files:"
  echo "$fmt"
  exit 1
fi
go vet ./...
go test -race -count=1 -p 1 -timeout 300s ./...
python3 -c "import re; open('/tmp/photinus-panel.js','w').write(re.search(r'<script>(.*?)</script>', open('cmd/photinus/panel.html').read(), re.S).group(1))"
node --check /tmp/photinus-panel.js
node --check cmd/photinus/sw.js
echo "preflight green"

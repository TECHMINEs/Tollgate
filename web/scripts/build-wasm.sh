#!/bin/sh
# Compiles the Go engine to WebAssembly for the browser demo.
set -e
cd "$(dirname "$0")/../.."
GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o web/public/tollgate.wasm ./cmd/tollgate-wasm
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" web/public/wasm_exec.js
echo "web/public/tollgate.wasm: $(du -h web/public/tollgate.wasm | cut -f1)"

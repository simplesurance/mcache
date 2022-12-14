#!/usr/bin/env bash
set -eu -o pipefail

dst="$(mktemp)"
go test -c -o "$dst"
"$dst" -test.run '^TestPutGetPerformance$' -test.v
pprof -http=:8080 "$dst" pprof.pb.gz
rm -- "$dst"

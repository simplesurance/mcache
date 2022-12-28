#!/usr/bin/env bash
set -eu -o pipefail
set -x

dst="$(mktemp)"
go test -c -o "$dst"
"$dst" -test.run '^TestPutGetPerformance$' -test.v
pprof -http=:8080 "$dst" pprof.pb.gz
rm -- "$dst"

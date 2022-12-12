#!/usr/bin/env bash
set -eu -o pipefail

go test -c -o mcache.test
./mcache.test -test.run '^TestPutGetPerformance$' -test.v
pprof -http=:8080 ./mcache.test pprof.pb.gz

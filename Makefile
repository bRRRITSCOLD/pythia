.PHONY: build test arch-test check-cgo

build:
	CGO_ENABLED=0 go build ./...

# arch-test runs the dependency-rule fitness guard with -count=1 so it is NEVER
# served from the Go test cache. The guard shells out to `go list` rather than
# importing internal/core (which may not compile / may hold a forbidden import),
# so core's sources are not test inputs and a plain cached `go test` would serve
# a stale PASS after a forbidden import is later added to core. -count=1 forces a
# re-run every time, keeping the guard's failure loud and reliable (see ADR-0004).
arch-test:
	go test -count=1 ./internal/arch/...

test: arch-test
	go test ./...

check-cgo:
	CGO_ENABLED=0 go build ./... && echo "CGO-free build OK"

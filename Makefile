.PHONY: build test check-cgo

build:
	CGO_ENABLED=0 go build ./...

test:
	go test ./...

check-cgo:
	CGO_ENABLED=0 go build ./... && echo "CGO-free build OK"

# GOWORK=off keeps a go.work file in a parent folder from changing this build.
export GOWORK := off

.PHONY: run build test vet fmt check

run:
	go run ./cmd/server

build:
	go build -o bin/server ./cmd/server

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

check: vet test
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:" && gofmt -l . && exit 1)

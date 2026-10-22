.PHONY: fmt-check vet test race build verify

fmt-check:
	@unformatted="$$(find . -name '*.go' -type f -print0 | xargs -0 gofmt -l)"; \
	test -z "$$unformatted" || (echo "$$unformatted" && exit 1)

vet:
	go vet ./...

test:
	go test ./... -count=1

race:
	go test -race -p 1 ./... -count=1

build:
	go build ./...

verify: fmt-check vet test race build

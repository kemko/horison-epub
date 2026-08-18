GO ?= go
GOFMT ?= gofmt
GOIMPORTS ?= goimports
GOLANGCI_LINT ?= golangci-lint
GOVULNCHECK ?= govulncheck

BINARY ?= bin/horizont-epub
COVERAGE_MIN ?= 80

.PHONY: build test test-race lint coverage vuln ci clean

build:
	mkdir -p "$(dir $(BINARY))"
	$(GO) build -trimpath -o "$(BINARY)" .

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

lint:
	@test -z "$$($(GOFMT) -l .)" || { echo "gofmt: files need formatting" >&2; $(GOFMT) -l .; exit 1; }
	@test -z "$$($(GOIMPORTS) -l .)" || { echo "goimports: files need formatting" >&2; $(GOIMPORTS) -l .; exit 1; }
	$(GOLANGCI_LINT) run --config .golangci.yml ./...

coverage:
	@set -eu; \
	coverage_file="$$(mktemp "$${TMPDIR:-/tmp}/horizont-epub-coverage.XXXXXX")"; \
	trap 'rm -f "$$coverage_file"' EXIT INT TERM; \
	$(GO) test -coverprofile="$$coverage_file" ./...; \
	$(GO) tool cover -func="$$coverage_file"; \
	coverage="$$( $(GO) tool cover -func="$$coverage_file" | awk '/^total:/ {gsub("%", "", $$3); print $$3}' )"; \
	awk -v coverage="$$coverage" -v minimum="$(COVERAGE_MIN)" 'BEGIN { if (coverage + 0 < minimum + 0) { printf "coverage %.1f%% is below minimum %.1f%%\n", coverage, minimum > "/dev/stderr"; exit 1 } }'

vuln:
	$(GOVULNCHECK) ./...

ci: build test-race lint coverage vuln

clean:
	rm -rf bin dist coverage.out coverage.html *.coverprofile

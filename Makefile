GO ?= go
GOFMT ?= gofmt
GOIMPORTS ?= goimports
GOLANGCI_LINT ?= golangci-lint
GOVULNCHECK ?= govulncheck
GORELEASER ?= goreleaser

BINARY ?= bin/horisont-epub
COVERAGE_MIN ?= 80
VULN_GO_VERSION ?= $(shell $(GO) list -m -f '{{.GoVersion}}')
VULN_GO_VERSION_NORMALIZED := $(if $(filter go%,$(VULN_GO_VERSION)),$(VULN_GO_VERSION),go$(VULN_GO_VERSION))

.PHONY: build test test-race lint coverage vuln release-check ci clean

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
	coverage_file="$$(mktemp "$${TMPDIR:-/tmp}/horisont-epub-coverage.XXXXXX")"; \
	trap 'rm -f "$$coverage_file"' EXIT INT TERM; \
	$(GO) test -coverprofile="$$coverage_file" ./...; \
	$(GO) tool cover -func="$$coverage_file"; \
	coverage="$$( $(GO) tool cover -func="$$coverage_file" | awk '/^total:/ {gsub("%", "", $$3); print $$3}' )"; \
	awk -v coverage="$$coverage" -v minimum="$(COVERAGE_MIN)" 'BEGIN { if (coverage + 0 < minimum + 0) { printf "coverage %.1f%% is below minimum %.1f%%\n", coverage, minimum > "/dev/stderr"; exit 1 } }'

vuln:
	GOVERSION=$(VULN_GO_VERSION_NORMALIZED) $(GOVULNCHECK) ./...

release-check:
	@set -eu; \
	if git remote get-url origin >/dev/null 2>&1; then \
		$(GORELEASER) check; \
		$(GORELEASER) release --snapshot --clean --skip=publish; \
	else \
		GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=remote.origin.url GIT_CONFIG_VALUE_0=https://github.com/local/horisont-epub.git $(GORELEASER) check; \
		GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=remote.origin.url GIT_CONFIG_VALUE_0=https://github.com/local/horisont-epub.git $(GORELEASER) release --snapshot --clean --skip=publish; \
	fi; \
		tar_count="$$(find dist -type f -name 'horisont-epub_*.tar.gz' -print | wc -l | tr -d ' ')"; \
		zip_count="$$(find dist -type f -name 'horisont-epub_*.zip' -print | wc -l | tr -d ' ')"; \
		checksum_count="$$(find dist -type f -name 'horisont-epub_*_checksums.txt' -print | wc -l | tr -d ' ')"; \
		test "$$tar_count" -eq 4; \
		test "$$zip_count" -eq 2; \
		test "$$checksum_count" -eq 1

ci: build test-race lint coverage vuln release-check

clean:
	rm -rf bin dist coverage.out coverage.html *.coverprofile

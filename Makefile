default: help

.PHONY: help
help: ## Show this help
	@echo
	@echo "Available commands:"
	@echo
	@awk -F ':|##' '/^[^\t].+?:.*?##/ {printf "\033[36m%-30s\033[0m %s\n", $$1, $$NF}' $(MAKEFILE_LIST)

BINDIR=$(shell go env GOPATH)
VERSION=$(shell cat VERSION)

# A build that is not the release says so, as hayami's does: released is HEAD
# exactly on this version's tag with nothing modified; anything else is
# `0.1.0-1a2b3c4-dev`.
COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null)
DIRTY=$(shell git status --porcelain 2>/dev/null | head -1)
ATTAG=$(shell git describe --exact-match --tags --match 'v$(VERSION)' HEAD 2>/dev/null)
BUILD_VERSION=$(if $(and $(ATTAG),$(if $(DIRTY),,x)),$(VERSION),$(VERSION)$(if $(COMMIT),-$(COMMIT))-dev)

LINT_NAME?=golangci-lint
LINT_VERSION?=v2.12.2
LINT_PROGRAM=$(LINT_NAME)-$(LINT_VERSION)

# Release asset coordinates for the pinned linter version.
# (The upstream install.sh is not used: its checksum extraction matches the
# .sbom.json asset line and fails verification on recent releases.)
# The checksums come from the same release as the tarball, so the checksums
# file is pinned here: a release replaced after this line was written fails.
# Dependabot does not track this; a new LINT_VERSION needs a new hash.
LINT_CHECKSUMS_SHA256=9accc7943a5b4be44416a7d4efa7efb3d18c7f1919d6581cc3536e185301a2d4
LINT_VERSION_NUM=$(LINT_VERSION:v%=%)
LINT_BASE_URL=https://github.com/golangci/golangci-lint/releases/download/$(LINT_VERSION)

.PHONY: install-lint
install-lint: $(BINDIR)/bin/$(LINT_PROGRAM) ## Install linter

$(BINDIR)/bin/$(LINT_PROGRAM):
	@echo "Setting up $(LINT_PROGRAM) ..."
	@set -e; \
	os=$$(uname -s | tr '[:upper:]' '[:lower:]'); \
	arch=$$(uname -m); \
	case "$$arch" in x86_64) arch=amd64;; aarch64|arm64) arch=arm64;; esac; \
	dist="$(LINT_NAME)-$(LINT_VERSION_NUM)-$$os-$$arch"; \
	tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	curl -fsSL "$(LINT_BASE_URL)/$$dist.tar.gz" -o "$$tmp/$$dist.tar.gz"; \
	curl -fsSL "$(LINT_BASE_URL)/$(LINT_NAME)-$(LINT_VERSION_NUM)-checksums.txt" -o "$$tmp/checksums.txt"; \
	sums=$$( (sha256sum "$$tmp/checksums.txt" 2>/dev/null || shasum -a 256 "$$tmp/checksums.txt") | awk '{print $$1}'); \
	if [ "$$sums" != "$(LINT_CHECKSUMS_SHA256)" ]; then echo "checksums file mismatch: want '$(LINT_CHECKSUMS_SHA256)' got '$$sums'"; exit 1; fi; \
	want=$$(awk -v f="$$dist.tar.gz" '$$2 == f {print $$1}' "$$tmp/checksums.txt"); \
	got=$$( (sha256sum "$$tmp/$$dist.tar.gz" 2>/dev/null || shasum -a 256 "$$tmp/$$dist.tar.gz") | awk '{print $$1}'); \
	if [ -z "$$want" ] || [ "$$want" != "$$got" ]; then echo "checksum mismatch for $$dist.tar.gz: want '$$want' got '$$got'"; exit 1; fi; \
	tar -C "$$tmp" -xzf "$$tmp/$$dist.tar.gz"; \
	mkdir -p "$(BINDIR)/bin"; \
	mv -v "$$tmp/$$dist/$(LINT_NAME)" "$(BINDIR)/bin/$(LINT_PROGRAM)"

.PHONY: setup
setup: install-lint ## Setup system for local development
	@echo "Make sure your system path includes GOPATH/bin."

# The linter refuses a Go newer than the one it was built with; go.mod
# carries no toolchain line, so the pin lives here (see hayami's Makefile).
LINT_GO_TOOLCHAIN?=go1.26.0

.PHONY: lint
# One linter cache per checkout: a cache shared between worktrees reports
# findings against files in worktrees that no longer exist.
lint: export GOTOOLCHAIN = $(LINT_GO_TOOLCHAIN)
lint: export GOLANGCI_LINT_CACHE = $(CURDIR)/.cache/golangci-lint
lint: install-lint ## Lint files
	@go version
	$(BINDIR)/bin/$(LINT_PROGRAM) run --timeout 5m0s --config config/.golangci-$(LINT_VERSION).yml ./...

.PHONY: check-no-binaries
check-no-binaries: ## Fail if a compiled binary is tracked in the repository
	@command -v file >/dev/null || { echo "check-no-binaries needs file(1)" >&2; exit 1; }; \
	found=$$( { git ls-files -z | xargs -0 -r file --mime-type -- 2>/dev/null \
	    | grep -E 'application/(x-(executable|sharedlib|pie-executable|archive|object|dosexec|coff|mach-binary|xz|bzip2)|gzip|zip|zstd|vnd\.microsoft\.portable-executable)' \
	    | cut -d: -f1; git ls-files '*.o' '*.a' '*.so' '*.syso'; } | sort -u); \
	if [ -n "$$found" ]; then \
	    echo "Compiled binaries are tracked in this repository:" >&2; \
	    echo "$$found" | sed 's/^/  /' >&2; \
	    exit 1; \
	fi; \
	echo "No compiled binaries are tracked."

.PHONY: test
test: ## Run the tests with the race detector
	go test -race ./...

.PHONY: coverage
coverage: ## Run the tests and open a coverage report
	go test -coverprofile=coverage.out ./... && go tool cover -html=coverage.out

LDFLAGS=-X github.com/ushineko/hikyaku/internal/buildinfo.version=$(BUILD_VERSION)

.PHONY: build
build: ## Build hikyaku for the host platform
	CGO_ENABLED=0 go build -trimpath -ldflags='$(LDFLAGS)' -o hikyaku ./cmd/hikyaku

# The release tarball: the one static binary, the README, the licence and
# the Slack app manifest. Linux amd64 only for now: the service is tested
# there, and the keyring and socket paths have not been tried elsewhere.
.PHONY: release
release: build ## Package the binary into dist/ as a tar.gz with SHA256SUMS
	@set -e; \
	rm -rf dist; mkdir -p dist; \
	base="hikyaku-$(VERSION)-linux-amd64"; stage="dist/$$base"; \
	mkdir -p "$$stage"; \
	cp hikyaku README.md LICENSE docs/slack-app-manifest.yaml "$$stage/"; \
	tar -C dist -czf "dist/$$base.tar.gz" "$$base"; \
	rm -rf "$$stage"; \
	cd dist && sha256sum *.tar.gz > SHA256SUMS
	@ls -l dist/*.tar.gz dist/SHA256SUMS

.PHONY: vuln
vuln: ## Scan for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: clean
clean: ## Remove build artifacts
	@rm -rf hikyaku dist/ coverage.out

###############################################################################
# Tools Finder
###############################################################################

golint := $(shell which golangci-lint)
ifeq ($(golint),)
golint := $(shell go env GOPATH)/bin/golangci-lint
endif

goreleaser := $(shell which goreleaser)
ifeq ($(goreleaser),)
goreleaser := $(shell go env GOPATH)/bin/goreleaser
endif

# Go lines formatter
golines := $(shell which golines)
ifeq ($(golines),)
golines := $(shell go env GOPATH)/bin/golines
endif

# Go imports formatter
goimports := $(shell which goimports)
ifeq ($(goimports),)
goimports := $(shell go env GOPATH)/bin/goimports
endif

wgo :=  $(shell which wgo)
ifeq ($(wgo),)
wgo := $(shell go env GOPATH)/bin/wgo
endif

# Mockery (mock generator)
mockery := $(shell which mockery)
ifeq ($(mockery),)
mockery := $(shell go env GOPATH)/bin/mockery
endif

# Cosign (container signing)
cosign := $(shell which cosign)
ifeq ($(cosign),)
cosign := $(shell go env GOPATH)/bin/cosign
endif

ifeq ($(DOCKER_HOST),)
	# DOCKER_HOST is not set, assume default Unix socket
	DOCKER_MOUNT := -v /var/run/docker.sock:/var/run/docker.sock
else
	# DOCKER_HOST is set, parse it to see if it's a Unix socket
	ifneq ($(findstring unix://,$(DOCKER_HOST)),)
		# It's a Unix socket, extract the path
		# Example: DOCKER_HOST=unix:///some/path/docker.sock
		DOCKER_SOCKET_PATH := $(subst unix://,,$(DOCKER_HOST))
		DOCKER_MOUNT := -v $(DOCKER_SOCKET_PATH):/var/run/docker.sock
	else
		# It's not a Unix socket (e.g., TCP), so no local socket mount is needed
		DOCKER_MOUNT :=
	endif
endif

GOMODCACHE := $(shell go env GOMODCACHE)
ifeq ($(GOMODCACHE),)
	GOMODCACHE := /var/tmp/cache/docker/mod
endif

GOBUILDCACHE := $(shell go env GOCACHE)
ifeq ($(GOBUILDCACHE),)
	GOBUILDCACHE := /var/tmp/cache/docker/build
endif

###############################################################################
# Build
###############################################################################

.PHONY: build
build: $(goreleaser)
	$(goreleaser) build --single-target --snapshot --clean

.PHONY: snapshot
snapshot: $(goreleaser)
	$(goreleaser) release --snapshot --clean

.PHONY: release
release: $(goreleaser) $(cosign)
	$(goreleaser) release --clean

.PHONY: run
run:
	go run ./main.go

###############################################################################
# Install missing tools
###############################################################################

$(golint):
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

$(goreleaser):
	go install github.com/goreleaser/goreleaser/v2@latest

$(golines):
	go install github.com/segmentio/golines@latest

$(goimports):
	go install golang.org/x/tools/cmd/goimports@latest

$(mockery):
	go install github.com/vektra/mockery/v3@latest

$(wgo):
	go install github.com/bokwoon95/wgo@latest

$(cosign):
	go install github.com/sigstore/cosign/v2/cmd/cosign@latest

###############################################################################
# Various commands
###############################################################################

.PHONY: watch
watch: $(wgo)
	$(wgo) -xdir "dist/" sh -c 'while nc -vz 127.0.0.1 38080 > /dev/null 2>&1; do sleep 1; done; make run || exit 1' --signal SIGTERM

.PHONY: unit
unit:
	go test -race -covermode=atomic -tags=test,unit -timeout=30s ./...

.PHONY: setup-integration
setup-integration:
	@cd integration && \
	./deploy.sh && \
	count=0; \
	while ! curl -fsSL http://localhost:38080/realms/master/protocol/openid-connect/certs > /dev/null 2>&1; do \
		if [ "$$count" -gt 30 ]; then \
			echo 'Keycloak failed to start' && \
			exit 1; \
		fi; \
		echo 'Waiting for Keycloak to start...' && \
		count=$$((count+1)) && \
		sleep 1; \
	done

# NB: Integration will also run unit tests.
.PHONY: integration
integration:
	@mkdir -p dist
	go test -race -covermode=atomic -coverprofile=dist/cover.out -tags=test,integration -timeout=30s ./...
	go tool cover -html dist/cover.out -o dist/cover.html

.PHONY: teardown-integration
teardown-integration:
	@cd integration && ./teardown.sh

.PHONY: lint
lint: $(golint)
	$(golint) run ./...

.PHONY: clean
clean:
	rm -rf dist/

.PHONY: fmt
fmt: $(golines) $(goimports)
	$(golines) -w .
	$(goimports) -w .

.PHONY: mocks
mocks: $(mockery)
	$(mockery)

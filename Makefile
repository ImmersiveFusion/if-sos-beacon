# Developer convenience targets. CI (.github/workflows/ci.yml) is the source of
# truth; these mirror it for local use. The binary/release path stays
# Snowglobe-matched (goreleaser + release.yml), so there is no release target
# here.

BINARY := sos-beacon
PKG := ./cmd/sos-beacon

.PHONY: build test lint fmt run tidy clean

build: ## Build the binary
	go build -o $(BINARY) $(PKG)

test: ## Run tests with the race detector and coverage
	go test -race -cover ./...

lint: ## Run go vet and golangci-lint
	go vet ./...
	golangci-lint run

fmt: ## Format with gofumpt
	gofumpt -w .

run: build ## Build and run one pass against config.yaml
	./$(BINARY) -config config.yaml

tidy: ## Tidy go.mod/go.sum
	go mod tidy

clean: ## Remove build artifacts
	rm -f $(BINARY) $(BINARY).exe

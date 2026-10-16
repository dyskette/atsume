BIN := bin/atsume
# Run the generator through the tool directive in go.mod, so its version is
# pinned with every other dependency. A templ on PATH drifts from the runtime
# the module compiles against, and the mismatch only shows up as undefined
# symbols in the generated code.
TEMPL := go tool templ

.PHONY: help dev generate build test check corpus clean rust-check rust-corpus

help:
	@grep -E '^[a-z-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

generate: ## regenerate templ components
	$(TEMPL) generate

dev: generate ## run with live reload on :8080
	$(TEMPL) generate --watch --proxy="http://localhost:8080" --cmd="go run ./cmd/atsume"

build: generate ## build a static binary
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/atsume

test: generate ## run the unit tests
	go test ./...

# The tests that need real modules (module loading, the golden templates, the
# app end to end) find the clone TestCorpus makes, so running them nightly
# shows whether upstream's current modules still work, not just their XPath.
corpus: generate ## run the upstream drift checks against a fresh FMD2 clone
	ATSUME_FETCH_CORPUS=1 go test ./internal/txquery/ -run TestCorpus -v
	go test ./internal/scraper/ ./internal/app/

check: generate ## everything CI runs
	gofmt -l . | tee /dev/stderr | (! read)
	git diff --exit-code
	go vet ./...
	go test ./...
	go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

# The Rust port. Clippy's warnings are errors here, as in CI.
rust-check: ## everything CI runs for the Rust crates
	cargo fmt --all --check
	cargo clippy --workspace --all-targets --locked -- -D warnings
	cargo test --workspace --locked
	cargo deny --locked check

# Tests that need upstream's modules are ignored by default, so a run without
# them shows them as ignored rather than passed. This runs only those.
rust-corpus: ## the Rust tests that need an FMD2 checkout in ATSUME_FMD2_DIR
	cargo test --workspace --locked --release -- --ignored

clean:
	rm -rf bin
	cargo clean

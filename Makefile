BIN := bin/atsume
# Run the generator through the tool directive in go.mod, so its version is
# pinned with every other dependency. A templ on PATH drifts from the runtime
# the module compiles against, and the mismatch only shows up as undefined
# symbols in the generated code.
TEMPL := go tool templ

.PHONY: help dev generate build test check corpus clean

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

corpus: generate ## run the upstream drift check against a fresh FMD2 clone
	ATSUME_FETCH_CORPUS=1 go test ./internal/txquery/ -run TestCorpus -v

check: generate ## everything CI runs
	gofmt -l . | tee /dev/stderr | (! read)
	git diff --exit-code
	go vet ./...
	go test ./...

clean:
	rm -rf bin

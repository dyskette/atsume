BIN := bin/atsume
TEMPL := $(shell go env GOPATH)/bin/templ

.PHONY: help dev generate build test check corpus clean tools

help:
	@grep -E '^[a-z-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

tools: ## install the code generators
	go install github.com/a-h/templ/cmd/templ@v0.3.906

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

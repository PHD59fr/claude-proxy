VERSION ?= $(shell printf '%X' $$(date +%s))
BINARY  := claude-proxy
GOFLAGS := -trimpath
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build test test-race vet fmt clean docker

all: build

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/claude-proxy

test:
	go test ./... -count=1 -timeout=60s

test-race:
	go test -race ./... -count=1 -timeout=60s

vet:
	go vet ./...

fmt:
	gofmt -s -w .

fmt-check:
	@test -z "$$(gofmt -s -l .)" || (echo "Files need formatting:" && gofmt -s -l . && exit 1)

clean:
	rm -f $(BINARY)

docker:
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY):$(VERSION) .

docker-run:
	docker run --rm -p 127.0.0.1:3000:3000 \
		-e UPSTREAM_BASE_URL=https://opencode.ai/zen/v1 \
		-e UPSTREAM_API_KEY=public \
		-e DEFAULT_MODEL=big-pickle \
		$(BINARY):$(VERSION)

docker-run-debug:
	docker run --rm -p 127.0.0.1:3000:3000 \
		-e UPSTREAM_BASE_URL=https://opencode.ai/zen/v1 \
		-e UPSTREAM_API_KEY=public \
		-e DEBUG=true \
		$(BINARY):$(VERSION)

docker-run-passthrough:
	docker run --rm -p 0.0.0.0:3000:3000 \
		-e UPSTREAM_BASE_URL=https://openrouter.ai/api \
		-e UPSTREAM_API_KEY_PASSTHROUGH=true \
		-e ALLOW_UNLISTED_MODELS=true \
		$(BINARY):$(VERSION)

install:
	CGO_ENABLED=0 go install $(GOFLAGS) -ldflags="$(LDFLAGS)" ./cmd/claude-proxy

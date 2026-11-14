COMPOSE = docker compose -f deploy/compose/compose.yaml

build-linux:      ## Static linux binaries for Dockerfile.local
	CGO_ENABLED=0 GOOS=linux go build -trimpath -o bin/ ./cmd/...

.PHONY: build-linux build test vet vectors cddl c-vectors c-size schemas up down logs smoke clean

build:            ## Build all Go binaries into ./bin
	go build -o bin/ ./cmd/...

test:             ## Unit tests and golden vectors (Go)
	go test ./...

vet:
	go vet ./...

vectors:          ## Regenerate golden vectors (a contract change: review the diff)
	go test ./internal/wire -update

cddl:             ## Validate golden vectors against spec/wire/envelope.cddl (needs zcbor)
	scripts/validate-vectors.sh

c-vectors:        ## Generate the C codec with zcbor and run vectors through it (needs zcbor, gcc)
	scripts/c-vectors.sh

c-size:           ## Check the device-side codec fits an nRF52840 (needs arm-none-eabi-gcc, zcbor)
	scripts/c-size-budget.sh

schemas:          ## Check schemas and generate Go and C constants
	go run ./cmd/iotgen gen -c sdk/c/schemas -go internal/schemas spec/schemas/*.yaml
	gofmt -w internal/schemas

up:               ## Start the dev stack
	$(COMPOSE) up -d --build

down:             ## Stop the dev stack (keeps volumes)
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f --tail=100

smoke:            ## Signed gateway relay through HAProxy mTLS to ingest
	$(COMPOSE) --profile tools run --rm simulator

clean:            ## Stop the stack and delete all its data
	$(COMPOSE) down -v
	rm -rf bin build sdk/c/generated

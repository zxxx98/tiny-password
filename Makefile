WEB := web
DIST := internal/webassets/dist
IMAGE := tiny-password:dev

.PHONY: build test test-race web-install web-build web-test web-typecheck \
        run docker-build test-e2e tidy clean

## build: build frontend assets then the Go binary
build: web-build
	go build -trimpath -o bin/tiny-password ./cmd/tiny-password

## test: vet + run all Go tests (assets dir must exist for go:embed)
test:
	go vet ./...
	go test ./...

test-race:
	go vet ./...
	go test -race ./...

web-install:
	npm --prefix $(WEB) ci

## web-build: install, typecheck, test, and build the bundle into $(DIST)
web-build: web-install web-typecheck web-test
	npm --prefix $(WEB) run build

web-typecheck: web-install
	npm --prefix $(WEB) run typecheck

web-test: web-install
	npm --prefix $(WEB) test -- --run

## run: build and run locally against a scratch data dir
run: build
	TP_DATA_DIR=/tmp/tiny-password-data TP_ADDR=127.0.0.1:8080 ./bin/tiny-password

docker-build:
	docker build -t $(IMAGE) .

## test-e2e: run the isolated test compose against an empty database and
## synthetic secrets; cleans up on exit.
## With E2E_SPEC=<file>, run that browser spec against a locally built
## server instead (Playwright; see scripts/test-browser-e2e.sh).
test-e2e:
ifeq ($(strip $(E2E_SPEC)),)
	$(MAKE) docker-build
	bash scripts/test-e2e.sh
else
	bash scripts/test-browser-e2e.sh $(E2E_SPEC)
endif

tidy:
	go mod tidy

clean:
	rm -rf bin

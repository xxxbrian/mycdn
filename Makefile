.PHONY: build web dev test race vet clean image api-types bench

# Prefer the SDK-aware compiler on macOS when a package-manager clang shadows it.
ifeq ($(shell uname -s),Darwin)
export CC := /usr/bin/clang
export CXX := /usr/bin/clang++
endif

build: web
	go build -trimpath -o bin/mycdn ./cmd/mycdn

web:
	pnpm --dir web install --frozen-lockfile
	pnpm --dir web build
	node scripts/embed-web.mjs

dev:
	go run ./cmd/mycdn -config data/config.json

test:
	go test ./...

race:
	go test -race ./internal/cache ./internal/rules ./internal/gateway ./internal/analytics ./internal/admin ./cmd/mycdn

vet:
	go vet ./...

image:
	docker build -t mycdn:local .

api-types:
	node scripts/generate-api.mjs

bench:
	go test ./internal/gateway -run '^$$' -bench BenchmarkCacheHit -benchmem

clean:
	go clean ./...

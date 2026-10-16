# syntax=docker/dockerfile:1
FROM node:24-bookworm-slim AS web
WORKDIR /src/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY . .
COPY --from=web /src/web/dist/ ./internal/admin/dist/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -mod=readonly -trimpath -ldflags="-s -w" -o /mycdn ./cmd/mycdn

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libstdc++6 \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --create-home --shell /usr/sbin/nologin mycdn \
    && mkdir /data && chown mycdn:mycdn /data
COPY --from=build /mycdn /usr/local/bin/mycdn
USER mycdn
WORKDIR /data
EXPOSE 8080 8081
VOLUME /data
ENTRYPOINT ["mycdn"]
CMD ["-config", "/data/config.json"]

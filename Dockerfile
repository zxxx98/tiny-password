# syntax=docker/dockerfile:1

# ---- Stage 1: build the React bundle -----------------------------------------
FROM node:22-alpine AS web
# Mirror the repository layout so the config's relative outDir
# (../internal/webassets/dist) resolves the same way it does locally.
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- Stage 2: build the Go binary and fetch the pinned 7-Zip -----------------
FROM golang:1.26-alpine AS go
WORKDIR /src
RUN apk add --no-cache xz
COPY go.mod go.mod
COPY go.sum* ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webassets/dist internal/webassets/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/tiny-password ./cmd/tiny-password
# Pinned 7-Zip static binary; SHA256 recorded in docs/decisions/0002.
RUN set -eu; \
    ARCH="$(uname -m)"; \
    case "$ARCH" in \
      x86_64)  TARBALL=7z2603-linux-x64.tar.xz;   SHA256=dc99eff5008f1ab79bd7084c68513701547a808a89502bf4133683535ab3c695 ;; \
      aarch64) TARBALL=7z2603-linux-arm64.tar.xz; SHA256=2389ba20e4d8295e8709c20b6263b69bd1ec4972fe38a04ad7a1badbf595b996 ;; \
      *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;; \
    esac; \
    wget -qO /tmp/7z.tar.xz "https://github.com/ip7z/7zip/releases/download/26.03/${TARBALL}"; \
    echo "${SHA256}  /tmp/7z.tar.xz" | sha256sum -c -; \
    xz -dc /tmp/7z.tar.xz | tar -xf - -C /out 7zz; \
    chmod 0555 /out/7zz; \
    rm /tmp/7z.tar.xz

# ---- Stage 3: minimal runtime -------------------------------------------------
# Debian (glibc) matches the official 7zz build in the builder stage; the
# pinned binary was probed on exactly this libc (D04, docs/decisions/0001).
FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata wget \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 10001 app \
    && useradd -u 10001 -g 10001 -M -d /nonexistent -s /usr/sbin/nologin app \
    && mkdir -p /data \
    && chown app:app /data \
    && chmod 0750 /data
COPY --from=go /out/tiny-password /usr/local/bin/tiny-password
COPY --from=go /out/7zz /usr/local/bin/7zz
USER app
EXPOSE 8080
VOLUME /data
ENV TP_ADDR=":8080" \
    TP_DATA_DIR=/data
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
ENTRYPOINT ["tiny-password"]

# syntax=docker/dockerfile:1
# Workers:   docker build --build-arg BIN=worker|director .
# Dashboard: docker build --build-arg BIN=web --target web .   (adds k9s)
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG BIN
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${BIN}

# k9s for the dashboard's embedded "Live from Kubernetes" panel, verified
# against the release checksums.
FROM alpine:3.22 AS k9s
ARG TARGETARCH
ARG K9S_VERSION=v0.51.0
RUN set -eu; \
    base="https://github.com/derailed/k9s/releases/download/${K9S_VERSION}"; \
    asset="k9s_Linux_${TARGETARCH}.tar.gz"; \
    cd /tmp; \
    wget -q "${base}/${asset}" "${base}/checksums.sha256"; \
    grep " ${asset}\$" checksums.sha256 | sha256sum -c -; \
    tar -xzf "${asset}" k9s; \
    install -m 0755 k9s /usr/local/bin/k9s

FROM gcr.io/distroless/static-debian12:nonroot AS web
COPY --from=k9s /usr/local/bin/k9s /usr/local/bin/k9s
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]

# Default target: workers (no k9s).
FROM gcr.io/distroless/static-debian12:nonroot AS app
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]

FROM golang:1.27-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG BUILD_VERSION=dev
ARG BUILD_COMMIT=

RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X main.buildVersion=${BUILD_VERSION} -X main.buildCommit=${BUILD_COMMIT}" \
    -o /out/coin-book-api ./cmd/coin-book-api
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/healthcheck ./cmd/healthcheck
# A named volume copies this directory's ownership on first mount; distroless has no shell to chown later.
RUN mkdir -p /out/data/imports

FROM gcr.io/distroless/static-debian13:nonroot

WORKDIR /app
COPY --from=builder /out/coin-book-api /usr/local/bin/coin-book-api
COPY --from=builder /out/healthcheck /bin/healthcheck
COPY --from=builder /src/config /app/config
COPY --from=builder --chown=65532:65532 /out/data /data

USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=4s --retries=3 CMD ["/bin/healthcheck"]

ENTRYPOINT ["/usr/local/bin/coin-book-api"]

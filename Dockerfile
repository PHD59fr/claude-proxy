FROM golang:1.26-alpine AS builder

RUN apk add --no-cache upx

WORKDIR /src

COPY go.mod go.sum* ./
RUN go mod download || true

COPY . .

ARG VERSION=dev

RUN CGO_ENABLED=0 \
    go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/claude-proxy \
      ./cmd/claude-proxy

RUN upx --best --lzma /out/claude-proxy || true

FROM scratch

COPY --from=builder \
  /etc/ssl/certs/ca-certificates.crt \
  /etc/ssl/certs/ca-certificates.crt

COPY --from=builder \
  /out/claude-proxy \
  /app/claude-proxy

WORKDIR /app

USER 65532:65532

EXPOSE 3000

ENTRYPOINT ["/app/claude-proxy"]
CMD ["serve", "--listen", "0.0.0.0:3000"]

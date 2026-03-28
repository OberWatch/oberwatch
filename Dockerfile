# Build stage
FROM golang:1.26-alpine AS builder
RUN apk add --no-cache git make nodejs npm

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

# Build dashboard
COPY dashboard/ dashboard/
RUN cd dashboard/svelte && npm ci && npm run build

# Build Go binary
COPY . .
RUN CGO_ENABLED=1 go build -ldflags="-s -w -X main.version=${VERSION}" -o oberwatch ./cmd/oberwatch

# Runtime stage
FROM alpine:3.20
RUN apk add --no-cache ca-certificates sqlite-libs
COPY --from=builder /app/oberwatch /usr/local/bin/oberwatch

EXPOSE 8080
VOLUME ["/data"]

ENTRYPOINT ["oberwatch"]
CMD ["serve", "--config", "/etc/oberwatch/oberwatch.toml"]

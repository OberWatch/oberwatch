# Build stage
FROM golang:1.26-alpine AS builder
RUN apk add --no-cache gcc musl-dev git make nodejs npm

WORKDIR /app
ARG CHANNEL=dev
ARG COMMIT=dev
COPY go.mod go.sum ./
RUN go mod download

# Build dashboard
COPY dashboard/ dashboard/
RUN cd dashboard/svelte && npm ci && npm run build

# Build Go binary
COPY . .
RUN cp -R dashboard/svelte/build/. internal/dashboard/static/ && \
    cp -R dashboard/svelte/static/. internal/dashboard/static/
RUN CGO_ENABLED=1 go build -ldflags="-s -w -X main.channel=${CHANNEL} -X main.commit=${COMMIT}" -o oberwatch ./cmd/oberwatch

# Runtime stage
FROM alpine:3.20
RUN apk add --no-cache ca-certificates sqlite-libs && \
    addgroup -S oberwatch && adduser -S oberwatch -G oberwatch && \
    mkdir -p /data && chown oberwatch:oberwatch /data
COPY --from=builder /app/oberwatch /usr/local/bin/oberwatch

WORKDIR /data
EXPOSE 8080
VOLUME ["/data"]

USER oberwatch
ENTRYPOINT ["oberwatch"]
CMD ["serve"]

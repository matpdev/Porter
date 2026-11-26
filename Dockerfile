FROM golang:1.24-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/porter ./cmd/porter
RUN go build -o /out/playwright-cli github.com/mxschmitt/playwright-go/cmd/playwright

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    git \
    fonts-liberation \
    fonts-noto-color-emoji \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/porter /usr/local/bin/porter
COPY --from=builder /out/playwright-cli /usr/local/bin/playwright-cli

# Install Playwright driver + headless Chromium and system dependencies
RUN playwright-cli install --with-deps chromium && rm -f /usr/local/bin/playwright-cli

WORKDIR /app
VOLUME ["/app/data"]
EXPOSE 8787

ENTRYPOINT ["porter"]
CMD ["daemon", "--db", "/app/data/app.db", "--media-dir", "/app/data/media"]

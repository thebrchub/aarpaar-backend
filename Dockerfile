# ================================
# Build stage
# ================================
FROM golang:1.25.5-alpine3.23 AS builder

WORKDIR /app

# 1. Install git (Required for fetching private modules)
RUN apk add --no-cache git

# 2. Receive the token from Railway Build Args
ARG GO_KIT_GITHUB_TOKEN

# 3. Configure Git to use the token
# This effectively logs you in to GitHub for all https:// requests
RUN git config --global url."https://${GO_KIT_GITHUB_TOKEN}@github.com/".insteadOf "https://github.com/"

# 4. Set GOPRIVATE to skip the public proxy for your repos
ENV GOPRIVATE=github.com/shivanand-burli/*

# 5. Download dependencies (Cached layer)
COPY ./go.mod ./go.sum ./
RUN go mod download

# 5.1 Force re-download go-starter-kit (bust Docker cache with CACHEBUST arg)
# Set CACHEBUST to a unique value per build (e.g. Railway: $RAILWAY_DEPLOYMENT_ID)
ARG CACHEBUST=0
RUN rm -rf "$(go env GOMODCACHE)/github.com/shivanand-burli" && \
    go mod download github.com/shivanand-burli/go-starter-kit

# 6. Build the application
COPY ./ .
RUN CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o app

# ================================
# Runtime stage
# ================================
# Using 'static' because CGO_ENABLED=0 creates a static binary
FROM gcr.io/distroless/static-debian12

WORKDIR /app

# Copy the binary from the builder stage
COPY --from=builder /app/app /app/app

# Copy the bot corpus file (not embedded in the Go binary)
COPY --from=builder /app/corpus/chat.tsv /app/corpus/chat.tsv

# Point the app at the corpus file
ENV BOT_CORPUS_PATH=/app/corpus/chat.tsv

# Documentation for Railway
EXPOSE 2028

# Start the app
CMD ["/app/app"]
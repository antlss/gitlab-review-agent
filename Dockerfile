# Build stage
FROM golang:1.25-alpine AS builder

# Install necessary build tools for CGO (sqlite3 requires gcc)
RUN apk add --no-cache git gcc musl-dev

WORKDIR /app

# Download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the server and CLI applications
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o /app/bin/server ./cmd/server
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o /app/bin/cli ./cmd/cli

# Final runtime stage
FROM alpine:latest

# Install runtime dependencies (git is required by the agent to run rev-parse, diff, etc.)
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

# Copy built binaries
COPY --from=builder /app/bin/server .
COPY --from=builder /app/bin/cli .

# Setup directories for local storage and git cloning operations
RUN mkdir -p /app/data /app/resource/repos

# Ensure the executable has run permissions
RUN chmod +x ./server ./cli

# Expose server port
EXPOSE 8080

CMD ["./server"]

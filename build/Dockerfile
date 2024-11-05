# ===================================================================================
# === Stage 1: Build the binary =====================================================
# ===================================================================================
FROM golang:1.23-alpine AS go-build

WORKDIR /build

# Fetch and cache Go modules
COPY go.mod .
COPY go.sum .
RUN go mod download

# Copy in Go source files
COPY cmd/ ./cmd/

# Now run the build
RUN go build -o mockery github.com/benc-uk/mockery/cmd

# ================================================================================================
# === Stage 2: Get binary into a lightweight container ===========================================
# ================================================================================================
FROM alpine:3.14
WORKDIR /app 

# Copy the Go server binary
COPY --from=go-build /build/mockery . 

EXPOSE 8000

ENTRYPOINT [ "./mockery" ]
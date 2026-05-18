# ---- Build stage ----
FROM golang:1.26-alpine AS builder

WORKDIR /src

# Cache deps first
COPY go.mod .
RUN go mod download

# Build a static binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build ./cmd/server

# ---- Runtime stage ----
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /src/server /server

EXPOSE 8000
USER nonroot:nonroot

ENTRYPOINT ["/server"]

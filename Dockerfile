# ========== Build Stage ==========
FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod ./
RUN go mod tidy
COPY . .

# build binary
RUN go build -o forecast .

# ========== Run Stage ==========
FROM alpine:latest

WORKDIR /root/
COPY --from=builder /app/forecast .

# use environment variable PORT (Render injects this automatically)
EXPOSE 8080
CMD ["./forecast"]

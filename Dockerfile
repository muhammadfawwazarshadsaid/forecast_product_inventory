# ========== Build Stage ==========
FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# build binary
RUN go build -o forecast .

# ========== Run Stage ==========
FROM alpine:latest

WORKDIR /root/
COPY --from=builder /app/forecast .

EXPOSE 8080
CMD ["./forecast"]

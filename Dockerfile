# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/proxycollector ./cmd/proxycollector

FROM alpine:3.23 AS certificates
RUN apk add --no-cache ca-certificates

FROM scratch
COPY --from=certificates /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/proxycollector /proxycollector
WORKDIR /app
EXPOSE 27298
ENTRYPOINT ["/proxycollector", "serve", "-c", "/etc/proxycollector/config.yaml"]

# syntax=docker/dockerfile:1

FROM golang:1.27.1-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/router-device-proxy ./cmd/router-device-proxy

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/router-device-proxy /router-device-proxy
ENV DATA_DIR=/data
EXPOSE 8090
ENTRYPOINT ["/router-device-proxy"]

# nodr serves the dashboard from internal/webui/dist, which is embedded into
# the binary at compile time. The web build therefore has to run before
# `go build`, which is why the web stage comes first.
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ .
RUN npm run build

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist/ /src/internal/webui/dist/
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/centopw/nodr/internal/buildinfo.Version=$(git describe --tags --always 2>/dev/null || echo dev)" \
    -o /out/nodr ./cmd/nodr

FROM alpine:3.20
RUN adduser -D -H -u 65532 nodr
COPY --from=build /out/nodr /usr/local/bin/nodr
USER nodr
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/nodr"]
CMD ["server", "-w", "/workspace", "--addr", "0.0.0.0:8080"]

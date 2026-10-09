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
# git is nodr's workspace ledger: first-run bootstrap runs `git init` and the
# initial commit with the host git binary, so it must exist in the image.
RUN apk add --no-cache git \
    && adduser -D -H -u 65532 nodr \
    && mkdir -p /var/lib/nodr \
    && chown 65532:65532 /var/lib/nodr \
    && chmod 0700 /var/lib/nodr
# Identity for the bootstrap ledger commit. Override these at run time to
# record your own author.
ENV GIT_AUTHOR_NAME=nodr \
    GIT_AUTHOR_EMAIL=nodr@localhost \
    GIT_COMMITTER_NAME=nodr \
    GIT_COMMITTER_EMAIL=nodr@localhost
COPY --from=build /out/nodr /usr/local/bin/nodr
USER nodr
EXPOSE 8080
# /var/lib/nodr holds bootstrap state, the generated KEK (when none is
# configured), and bootstrap-created workspaces. Mount a persistent volume there.
ENTRYPOINT ["/usr/local/bin/nodr"]
CMD ["server", "--data-dir", "/var/lib/nodr", "--addr", "0.0.0.0:8080"]

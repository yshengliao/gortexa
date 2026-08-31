# syntax=docker/dockerfile:1

# Build stage — generates proto and compiles a static binary.
FROM golang:1.27 AS build
WORKDIR /src

# Module proxy settings (mirror install.sh). Override at build time if your
# network policy differs.
ENV GOFLAGS=-mod=mod \
    GOPROXY=https://proxy.golang.org,direct \
    GOSUMDB=sum.golang.org \
    GOPRIVATE= \
    GOTOOLCHAIN=auto

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# gen/ is committed, but regenerate inside the image anyway using the pinned
# tool directives (tools/go.mod) so the build never trusts stale artifacts.
RUN go -C tools install tool \
 && PATH="$(go env GOPATH)/bin:$PATH" buf generate
RUN CGO_ENABLED=0 go build -o /out/gortexa ./cmd/server

# Runtime stage — minimal image.
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/gortexa /gortexa
COPY etc/config.yaml /etc/gortexa/config.yaml
ENV GORTEXA_CONFIG=/etc/gortexa/config.yaml
EXPOSE 8080
ENTRYPOINT ["/gortexa"]

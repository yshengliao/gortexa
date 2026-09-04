# syntax=docker/dockerfile:1

# Build stage — compiles a static binary.
# Pinned by digest: a floating tag means two builds a week apart produce
# different images from identical source. Refresh with the tag when bumping Go.
FROM golang:1.27@sha256:512690a5660563b57d37ecc31129e7f136e831db2aed24a1dbeb8ad7380dc0fa AS build
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
# gen/ is committed and CI fails on drift against proto/, so regenerating here
# bought a guarantee CI already provides while making every image build depend
# on reaching the buf module registry. Build from the committed tree instead.
# -trimpath keeps absolute build paths out of the binary; -s -w drops the
# symbol table and DWARF, which the runtime image has no use for.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gortexa ./cmd/server

# Runtime stage — minimal image.
# The :nonroot variant, pinned by digest. The untagged image runs as uid 0; a
# static Go binary needs no root, so there is nothing to trade away.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/gortexa /gortexa
# The config carries a placeholder JWT secret the server refuses to boot with,
# so this layer holds no credential: inject the real one as
# GORTEXA_AUTH__JWT_SECRET at runtime.
COPY etc/config.yaml /etc/gortexa/config.yaml
ENV GORTEXA_CONFIG=/etc/gortexa/config.yaml
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/gortexa"]

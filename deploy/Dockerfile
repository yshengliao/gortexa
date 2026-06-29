# syntax=docker/dockerfile:1

# Build stage — generates proto and compiles a static binary.
FROM golang:1.25 AS build
WORKDIR /src

# Module proxy settings (mirror setup.sh). Override at build time if your
# network policy differs.
ENV GOFLAGS=-mod=mod \
    GOPROXY=https://proxy.golang.org,direct \
    GOSUMDB=sum.golang.org \
    GOPRIVATE= \
    GOTOOLCHAIN=auto

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# gen/ is gitignored; regenerate inside the image.
RUN go install github.com/bufbuild/buf/cmd/buf@latest \
 && go install google.golang.org/protobuf/cmd/protoc-gen-go@latest \
 && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest \
 && go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@v2.29.0 \
 && go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2@v2.29.0 \
 && PATH="$(go env GOPATH)/bin:$PATH" buf generate
RUN CGO_ENABLED=0 go build -o /out/gortexa ./cmd/server

# Runtime stage — minimal image.
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/gortexa /gortexa
COPY etc/config.yaml /etc/gortexa/config.yaml
ENV GORTEXA_CONFIG=/etc/gortexa/config.yaml
EXPOSE 8080
ENTRYPOINT ["/gortexa"]

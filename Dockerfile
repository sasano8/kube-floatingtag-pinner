# Build on the native platform and cross-compile, so multi-arch builds do not need emulation.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS TARGETARCH COMMIT
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.commit=${COMMIT}" -o /out/pinner ./cmd/pinner

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pinner /pinner
ENTRYPOINT ["/pinner"]

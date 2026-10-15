# Build on the native platform and cross-compile, so multi-arch builds do not need emulation.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS TARGETARCH COMMIT
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.commit=${COMMIT}" -o /out/pinner ./cmd/pinner
# A credential file with no credentials in it: PINNER_CREDENTIAL points at it, so that pinner always finds a file.
# (The final image has no shell, so it is made here.)
RUN mkdir /out/credential && echo '{"auths":{}}' > /out/credential/config.json

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pinner /pinner
COPY --from=build --chown=nonroot:nonroot /out/credential /credential
# Mount the registry credentials (docker config.json format) over /credential/config.json; to use another file,
# set PINNER_CREDENTIAL (empty: no credentials).
ENV PINNER_CREDENTIAL=/credential/config.json
ENTRYPOINT ["/pinner"]

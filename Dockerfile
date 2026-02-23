FROM --platform=$BUILDPLATFORM golang:1.22 AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/spillway ./cmd/spillway

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/spillway /spillway
USER nonroot:nonroot
ENTRYPOINT ["/spillway"]

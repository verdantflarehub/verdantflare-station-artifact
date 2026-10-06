# syntax=docker/dockerfile:1

FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags='-s -w' \
    -o /out/station-artifact ./cmd/station-artifact

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/station-artifact /station-artifact
USER nonroot:nonroot
EXPOSE 5055
ENTRYPOINT ["/station-artifact"]

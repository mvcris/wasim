FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /wasim ./cmd/wasim
RUN install -d -o 65532 -g 65532 /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /wasim /wasim
COPY --from=build --chown=65532:65532 /data /data
ENV WASIM_GRAPH_ADDR=0.0.0.0:7070 \
    WASIM_CONSOLE_ADDR=0.0.0.0:7071 \
    WASIM_DB_PATH=/data/wasim.db \
    WASIM_MEDIA_DIR=/data/media
VOLUME /data
EXPOSE 7070 7071
ENTRYPOINT ["/wasim"]

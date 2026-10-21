# syntax=docker/dockerfile:1.7
FROM golang:1.27-alpine AS build
WORKDIR /src
ENV GOWORK=off
COPY libs/go/go.mod libs/go/go.sum ./libs/go/
COPY services/hello-service/go.mod services/hello-service/go.sum ./services/hello-service/
COPY libs/go ./libs/go
COPY services/hello-service ./services/hello-service
WORKDIR /src/services/hello-service
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/nexora-exchange/nexora/libs/go/version.Version=${VERSION} -X github.com/nexora-exchange/nexora/libs/go/version.Commit=${COMMIT} -X github.com/nexora-exchange/nexora/libs/go/version.BuildDate=${BUILD_DATE}" \
    -o /out/hello-service .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/hello-service /usr/local/bin/hello-service
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/hello-service"]

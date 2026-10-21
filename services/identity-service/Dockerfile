# syntax=docker/dockerfile:1.7
FROM golang:1.27-alpine AS build
WORKDIR /src
ENV GOWORK=off
COPY gen/go ./gen/go
COPY libs/go ./libs/go
COPY services/identity-service ./services/identity-service
WORKDIR /src/services/identity-service
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/nexora-exchange/nexora/libs/go/version.Version=${VERSION} -X github.com/nexora-exchange/nexora/libs/go/version.Commit=${COMMIT} -X github.com/nexora-exchange/nexora/libs/go/version.BuildDate=${BUILD_DATE}" \
    -o /out/identity-service .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/identity-service /usr/local/bin/identity-service
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/usr/local/bin/identity-service"]

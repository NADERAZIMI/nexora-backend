# syntax=docker/dockerfile:1.7
FROM rust:1.99-slim-bookworm AS build
# hadolint ignore=DL3008
RUN apt-get update \
    && apt-get install -y --no-install-recommends pkg-config cmake perl build-essential \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY Cargo.toml Cargo.lock ./
COPY crates ./crates
COPY services/core-template ./services/core-template
RUN cargo build --release --locked -p core-template

FROM gcr.io/distroless/cc-debian12:nonroot
COPY --from=build /src/target/release/core-template /usr/local/bin/core-template
USER 65532:65532
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/core-template"]

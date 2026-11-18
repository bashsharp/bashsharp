# Standalone source build: no umbrella checkout or sibling module overlays.
ARG BUILD_IMAGE=localhost/bashy-build:v1.0.0-rc.37-go1.27.2-c99fcb7318b2
FROM ${BUILD_IMAGE} AS build
WORKDIR /work
COPY --chown=10001:10001 . .
ENV BASHY_BUILD_RUN=1 GOWORK=off GOFLAGS=-p=1 GOMAXPROCS=8 GOMEMLIMIT=2GiB BASHY=/bashy
RUN /bashy dag --no-journal --force build binary-smoke

FROM build AS test
RUN /bashy dag --no-journal --force test

FROM build AS binaries
ENTRYPOINT ["/work/bin/bashsharp"]

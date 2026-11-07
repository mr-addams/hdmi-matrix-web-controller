# One hdmi-ui binary on distroless static-debian13: no shell, no package manager, no libc.
# The build is static (CGO disabled), so the final image contains nothing but the service itself.

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY static ./static
# The data directory is created here so that a named volume inherits owner 65532 on first mount.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/hdmi-ui . \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/hdmi-ui /hdmi-ui
COPY --from=build --chown=65532:65532 /out/data /data
ENV CONFIG_PATH=/data/config.json LISTEN_ADDR=:8080
# Explicit user: a future base image with a root default must not silently run the process as root.
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/hdmi-ui"]

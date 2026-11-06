# Один бинарник hdmi-ui внутри distroless static-debian13: без shell, без пакетного менеджера и libc.
# Сборка статическая (CGO выключен), поэтому финальный образ не содержит ничего, кроме самого сервиса.

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY static ./static
# Каталог данных создаём здесь: named volume унаследует владельца 65532 при первом монтировании.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/hdmi-ui . \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/hdmi-ui /hdmi-ui
COPY --from=build --chown=65532:65532 /out/data /data
ENV CONFIG_PATH=/data/config.json LISTEN_ADDR=:8080
# Явный пользователь: база с root-вариантом не сделает процесс root незаметно.
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/hdmi-ui"]

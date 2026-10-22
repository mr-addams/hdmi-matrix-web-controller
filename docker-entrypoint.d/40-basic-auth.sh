#!/bin/sh
# Файл паролей создаём при каждом старте из переменных окружения: пароль не попадает
# в образ, и его меняют через compose без пересборки.
set -eu
htpasswd -bc /etc/nginx/.htpasswd "${UI_USER:-admin}" "${UI_PASSWORD:-matrix}"
chmod 644 /etc/nginx/.htpasswd

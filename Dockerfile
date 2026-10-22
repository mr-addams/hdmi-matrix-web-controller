# Образ нужен только ради htpasswd: в nginx:stable-alpine его нет, а без хэша пароля
# basic auth не поднять. Остальное — стандартный nginx.
FROM nginx:stable-alpine
RUN apk add --no-cache apache2-utils
COPY docker-entrypoint.d/40-basic-auth.sh /docker-entrypoint.d/40-basic-auth.sh
RUN chmod +x /docker-entrypoint.d/40-basic-auth.sh

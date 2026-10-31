# srvmon за nginx на хосте

Шаблон `srvmon.conf` нужен для режима `SRVMON_PROXY=external`. В этом режиме Caddy не запускается, srvmon слушает `127.0.0.1:8088`, а TLS терминирует nginx, который уже работает на хосте.

## Установка сайта

```bash
sed "s/srvmon.example.com/$DOMAIN/g" /opt/srvmon/nginx/srvmon.conf > <каталог сайтов nginx>/$DOMAIN.conf
# сертификат через тот же webroot, что у остальных сайтов
docker run --rm -v <letsencrypt>:/etc/letsencrypt -v <webroot>:/var/www/certbot \
  certbot/certbot certonly --webroot -w /var/www/certbot -d "$DOMAIN" --non-interactive --agree-tos
nginx -t && nginx -s reload
```

## Реальный IP за stream-роутером

Если TCP/443 держит `stream` с `ssl_preread`, который передаёт HTTPS на `127.0.0.1:8443`, то все клиенты приходят с адреса 127.0.0.1. Тогда лимит попыток входа становится общим на всех, а fail2ban банит бесполезно. Чтобы srvmon видел настоящий IP, роутер должен передавать PROXY protocol.

Сам `proxy_protocol on` действует на весь stream-сервер, то есть на каждый бэкенд. Бэкенд, который PROXY-заголовок не понимает (например, TURN/TLS LiveKit), ставится за внутренний stream-сервер, который этот заголовок снимает:

```nginx
stream {
    map $ssl_preread_alpn_protocols $tls_backend {
        ~\bh2\b         127.0.0.1:8443;
        ~\bhttp/1\.1\b  127.0.0.1:8443;
        default         127.0.0.1:5350;   # было 127.0.0.1:5349
    }

    server {
        listen 443;
        ssl_preread on;
        proxy_protocol on;                # новое
        proxy_pass $tls_backend;
    }

    # Снимает PROXY-заголовок для бэкенда, который его не понимает.
    server {
        listen 127.0.0.1:5350 proxy_protocol;
        proxy_pass 127.0.0.1:5349;
        proxy_timeout 1h;
    }
}
```

Кроме того, у одного из сайтов на `127.0.0.1:8443` нужно написать `listen 127.0.0.1:8443 ssl proxy_protocol;`. Параметр относится к сокету, поэтому начинает действовать для всех сайтов на этом адресе. После этого любое подключение к `127.0.0.1:8443` напрямую, без роутера (локальные healthcheck'и, curl), должно отправлять PROXY-заголовок. `srvmon.conf` подхватывает `$proxy_protocol_addr` сам, если он задан. Если нет, используется `$remote_addr`.

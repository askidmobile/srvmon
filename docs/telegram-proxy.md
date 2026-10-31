# Telegram-бот через прокси на Cloudflare Workers

*English summary: if the monitoring server cannot reach `api.telegram.org` (common on Russian hosting), deploy the Worker below on Cloudflare's free plan and set `SRVMON_TELEGRAM_API=https://<worker>.<subdomain>.workers.dev` in `/opt/srvmon/.env`, then `docker compose up -d server`.*

С российских хостингов `api.telegram.org` часто недоступен: запросы бота висят до таймаута, а в панели (алерты → telegram-бот) при проверке токена пишется «нет связи с Bot API». srvmon умеет ходить в Bot API через прокси: адрес задаётся переменной `SRVMON_TELEGRAM_API`. Проще всего поднять такой прокси бесплатно на Cloudflare Workers.

## Как это работает

srvmon отправляет запросы вида `<SRVMON_TELEGRAM_API>/bot<токен>/<метод>`. Worker принимает их на своём адресе `*.workers.dev` и пересылает на `https://api.telegram.org` без изменений. Токен бота проходит через Worker, но нигде не сохраняется; Worker работает в вашем аккаунте Cloudflare.

## 1. Создать Worker

1. Зарегистрируйтесь на [dash.cloudflare.com](https://dash.cloudflare.com) (бесплатного плана хватает: 100 000 запросов в сутки, бот делает несколько тысяч).
2. **Workers & Pages** → **Create** → **Create Worker** (шаблон «Hello World»). Задайте имя, например `tg-proxy`, и нажмите **Deploy**.
3. **Edit code**, замените весь код на этот и снова **Deploy**:

```js
// Bot API proxy for srvmon: forwards /bot<token>/<method> to api.telegram.org.
export default {
  async fetch(request) {
    const url = new URL(request.url);
    if (!url.pathname.startsWith("/bot")) {
      return new Response("not found", { status: 404 });
    }
    const target = "https://api.telegram.org" + url.pathname + url.search;
    return fetch(new Request(target, request));
  },
};
```

Адрес Worker'а показан на его странице: `https://tg-proxy.<ваш-поддомен>.workers.dev`.

## 2. Проверить с сервера мониторинга

```bash
curl -sS https://tg-proxy.<ваш-поддомен>.workers.dev/bot<ТОКЕН>/getMe
```

Ответ `{"ok":true,"result":{...}}` значит, что прокси работает. Если `curl` висит, а из браузера на компьютере адрес открывается, см. «Если не работает» ниже.

## 3. Подключить в srvmon

В `/opt/srvmon/.env` на сервере мониторинга добавьте строку (без `/` в конце):

```bash
SRVMON_TELEGRAM_API=https://tg-proxy.<ваш-поддомен>.workers.dev
```

и перезапустите сервер:

```bash
cd /opt/srvmon && docker compose up -d server
```

Деплой (`remote-deploy.sh`) этот параметр не трогает, он переживает обновления. Дальше подключите бота как обычно: алерты → telegram-бот → токен → «сохранить» → `/start` боту → «использовать этот чат» → «отправить тест».

## Если не работает

- **`curl` из шага 2 висит, а в браузере всё открывается.** Некоторые провайдеры режут TLS-рукопожатие с постквантовым обменом ключей (ClientHello не помещается в один пакет). srvmon сам использует классический обмен (X25519), поэтому бот может работать, даже если ваш `curl` нет. Проверьте с `curl --curves X25519 ...` (curl 7.73+).
- **`*.workers.dev` заблокирован целиком.** Привяжите к Worker'у свой домен, заведённый в Cloudflare: страница Worker'а → **Settings** → **Domains & Routes** → **Add** → **Custom domain** (например `tg.example.com`), и укажите его в `SRVMON_TELEGRAM_API`.
- **В панели по-прежнему «нет связи с Bot API».** Убедитесь, что переменная дошла до контейнера: `docker inspect $(docker compose ps -q server) | grep TELEGRAM`, и посмотрите `docker compose logs --tail=50 server`.

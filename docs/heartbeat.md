# Пульс панели: подключение внешней проверки

*English summary: the panel pings an external URL once a minute (see "Panel heartbeat" in the README). Any service with a heartbeat / push URL works. healthchecks.io signs you in only with a link sent by email, and for some addresses that email never arrives; in that case use Better Stack, which signs in with Google: Uptime → Heartbeats → Create heartbeat, period 1 minute, grace 5 minutes, then paste its URL in alerts → panel heartbeat and press "ping now". Better Stack understands `<url>/fail`, so keep "report problems to URL/fail" on.*

Алерты шлёт сама панель, поэтому о её собственном падении должен сообщить кто-то снаружи. Раз в минуту `srvmon-server` проверяет базу, движок алертов, VictoriaMetrics и Telegram-бота и отправляет GET на внешний URL. Если какая-то проверка не проходит 3 минуты подряд, панель отправляет POST на `<url>/fail` со списком проблем. Если панель или её сервер ляжет, пинги прекратятся, и внешний сервис через несколько минут поднимет тревогу.

Подойдёт любой сервис с heartbeat- или push-URL. Ниже два проверенных варианта.

## Вариант 1. Better Stack (вход через Google)

1. Откройте [betterstack.com](https://betterstack.com) и войдите через Google.
2. На вопросе «How would you like to use Better Stack?» выберите **Uptime monitoring** и нажмите Continue.
3. «What integrations do you want to use?» ни на что не влияет, но без выбора Continue не активна: отметьте любую, например Cloudflare.
4. «Where can we call you?»: нажмите **Continue without a test call**. Если без URL не пускает, укажите `https://<ваш-домен>/healthz`: это вторая проверка снаружи, она заметит, если панель недоступна из интернета.
5. Тариф: хватит **Free** (10 heartbeats, 10 мониторов, уведомления на почту и в Slack).
6. В левом меню Uptime откройте **Heartbeats** → **Create heartbeat**. В поле **What service will this heartbeat track?** впишите `srvmon panel`. **Expect a heartbeat every**: `1` и единицу **minutes**, по умолчанию стоит **day**, обязательно смените. **with a grace period of**: 5 minutes.
7. В блоке **How should we notify the primary responder?** оставьте **E-mail**. Звонки и SMS есть только на платном тарифе. Нажмите **Create heartbeat**.
8. Скопируйте URL вида `https://uptime.betterstack.com/api/v1/heartbeat/<token>`.
9. В панели: **алерты → пульс панели** → вставьте URL → **сохранить** → **пинг сейчас**. Статус должен стать «● ок», а в Better Stack heartbeat перейдёт в Up.

Галку «сообщать о проблемах на URL/fail» оставьте включённой: Better Stack принимает `<url>/fail` и сразу отмечает heartbeat как упавший.

## Вариант 2. healthchecks.io

1. Зарегистрируйтесь на [healthchecks.io](https://healthchecks.io). Вход здесь только по ссылке из письма. Если письмо не приходит (проверьте «Спам»; на корпоративном Google Workspace его может задержать администратор), используйте вариант 1.
2. **Add Check** → **Schedule**: Period 1 minute, Grace 5 minutes.
3. В **Integrations** выберите каналы: почта включена сразу, есть Telegram, ntfy и другие.
4. Скопируйте Ping URL вида `https://hc-ping.com/<uuid>` и вставьте его в панели, как в последнем шаге варианта 1.

## Другие приёмники

Push-монитор Uptime Kuma, ваш второй сервер или любой другой URL тоже подойдут. Если приёмник не понимает `<url>/fail`, снимите галку «сообщать о проблемах на URL/fail»: тогда при проблемах панель просто перестанет пинговать, и приёмник сработает по своему таймауту.

## Если пинг не уходит

В панели в строке «статус» видна ошибка последней попытки. «нет связи» значит, что сервер панели не достаёт до сервиса (например, его блокирует хостинг). В таком случае можно проксировать пинг через Cloudflare Worker, как это сделано для Telegram в [telegram-proxy.md](telegram-proxy.md), или выбрать другой сервис.

URL пинга работает как пароль: панель хранит его зашифрованным мастер-ключом и показывает только хост и последние символы.

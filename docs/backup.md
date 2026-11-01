# Бэкап панели и аварийный доступ

*English summary: srvmon backs up its SQLite database on a schedule to any S3-compatible storage (settings → backup). Backups are encrypted with a key derived from the master key and never contain the master key itself, so export the master key once (settings → backup → show master key, or `srvmon-server master-key`) and keep it offline. To restore on a new host: deploy, stop `server`, `master-key -import`, `restore`, start. Emergency access: add your own SSH public keys in settings → emergency access; the panel keeps them in `authorized_keys` of its SSH user on every server, so losing the panel never locks you out.*

## Что попадает в бэкап

| Что | В бэкапе | Почему |
| --- | --- | --- |
| `srvmon.db`: серверы, SSH-ключи панели (зашифрованы), правила алертов, настройки, токены, чаты и задачи агента, аудит | да | без этого панель не поднять |
| `master.key` | **нет, никогда** | им шифруется сам бэкап и секреты в базе; храните его отдельно, офлайн |
| история метрик VictoriaMetrics | нет | её не жалко потерять, а весит она гигабайты |
| контекст моделей AI (том `ai-data`) | нет | история чатов, видимая в панели, лежит в `srvmon.db` |

Снимок базы делается через `VACUUM INTO`, то есть консистентно и без остановки панели. Архив (tar.gz с `manifest.json` и `srvmon.db`) шифруется XChaCha20-Poly1305 ключом, выведенным из мастер-ключа через HKDF (`srvmon backup v1`). Хранилище видит только шифротекст. Файлы называются `<префикс>srvmon-20261101T031500Z.srvmon-backup`.

## Настройка

1. Создайте бакет в любом S3-совместимом хранилище: Yandex Object Storage, AWS S3, Cloudflare R2, Backblaze B2, Selectel, MinIO. Лучше у другого провайдера, чем сервер мониторинга.
2. Создайте ключ доступа только к этому бакету. Нужны права на запись, чтение, список и удаление (удаление нужно, чтобы держать N последних копий). Можно включить версионирование или object lock, тогда удалённая копия останется в версиях.
3. В панели: `[6] настройки → бэкап`. Укажите endpoint (например `storage.yandexcloud.net`, регион `ru-central1`), бакет, префикс, ключ и секрет. Нажмите «проверить хранилище»: панель запишет, прочитает и удалит тестовый объект. Затем «сохранить» и «сделать бэкап сейчас».
4. По умолчанию бэкап делается раз в 24 часа, хранится 14 копий. Если бэкап не удался, панель повторяет попытку каждый час, а в Telegram приходит алерт «бэкап панели не удался» (правило `backup` на экране алертов, не чаще раза в сутки).
5. **Выгрузите мастер-ключ**: там же, «показать мастер-ключ» с кодом из Authenticator. Сохраните строку base64 в менеджере паролей. Пока ключ не выгружен, в настройках висит предупреждение. Из консоли: `docker compose exec server /srvmon-server master-key`.

Секрет S3 хранится в базе зашифрованным мастер-ключом и в ответах API не возвращается.

## Восстановление на новом сервере

```sh
# 1. Разверните srvmon как обычно (README, «Быстрый старт» или CI/CD) и остановите панель
cd /opt/srvmon
docker compose stop server

# 2. Верните мастер-ключ (строка base64, которую вы сохранили)
echo 'ВАШ_КЛЮЧ_BASE64' | docker compose run --rm -T server master-key -import

# 3a. Последний бэкап из S3
docker compose run --rm -e SRVMON_S3_SECRET_KEY='секрет' server restore \
  -s3-endpoint storage.yandexcloud.net -s3-region ru-central1 \
  -s3-bucket my-srvmon-backups -s3-prefix srvmon/ -s3-access-key AKIA... -force
#    (-list покажет все копии, -object <ключ> выберет конкретную)

# 3b. Или из скачанного файла
docker compose run --rm -T server restore -file - -force < srvmon-20261101T031500Z.srvmon-backup

# 4. Запустите панель
docker compose up -d
```

`restore` сначала расшифровывает и проверяет бэкап и только потом подменяет базу. Прежняя база остаётся рядом как `srvmon.db.before-restore-<время>`. Агенты на серверах продолжат слать метрики, если новый сервер мониторинга доступен по тому же домену: их токены лежат в восстановленной базе. Агент самого сервера мониторинга получит новый токен автоматически.

## Аварийный доступ к серверам

Панель ходит на серверы под пользователем `srvmon` своим ключом, который лежит только в её базе. Если пропадут и панель, и мастер-ключ, вход на серверы остаётся только через консоль провайдера. Чтобы этого не случилось:

1. `[6] настройки → аварийный доступ`: вставьте один или несколько **публичных** ключей, например `~/.ssh/id_ed25519.pub` с ноутбука или ключ с YubiKey (`ssh-keygen -t ed25519-sk`).
2. «Сохранить и применить»: панель добавит их в `~srvmon/.ssh/authorized_keys` на всех серверах с SSH-доступом и покажет статус по каждому. Новые серверы получают ключи при добавлении.
3. Проверьте: `ssh srvmon@<сервер>` с ноутбука. У `srvmon` есть доступ к docker; root через `sudo -i` есть только в режиме «полный sudo» (переключается в паспорте сервера), в «ограниченном» доступны systemctl, journalctl и `sudo docker`.

Ключи пишутся в отдельный блок между строками-маркерами `# srvmon emergency keys: begin …` / `… end`. Панель переписывает только этот блок, ключ панели и остальные строки не трогает. Пустой список удаляет блок. Опции вроде `from="…"` отбрасываются, RSA короче 3072 бит и DSA не принимаются.

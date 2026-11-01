// Translations of the HTTP/TCP/TLS checks and domain expiry, kept apart so
// the feature's strings sit together. Merged into ru / zh in lib/i18n.ts.
import type { Dict } from "@/lib/i18n";

export const checksRu: Dict = {
  "{n} day ago|{n} days ago": "{n} день назад|{n} дня назад|{n} дней назад",
  "waiting for the first check": "ждёт первой проверки",
  "port open": "порт открыт",
  "down": "не отвечает",
  "for {time}": "уже {time}",
  "latency": "задержка",
  "expiry unknown": "срок неизвестен",
  "expiry unknown: {err}": "срок неизвестен: {err}",
  "One target per line: https://site/path (status and certificate), tls://host:port (certificate), host:port (TCP port).":
    "По одной цели в строке: https://сайт/путь (ответ и сертификат), tls://host:port (сертификат), host:port (TCP-порт).",
  "{n} down": "не отвечает: {n}",
  "all up": "всё отвечает",
  "checks": "проверки",
  "No checks. Add the server's sites and ports: the panel checks them every minute and alerts when they are down, when a certificate or a domain is about to expire.":
    "Проверок нет. Добавьте сайты и порты сервера: панель проверяет их раз в минуту и присылает алерт, если они не отвечают и когда истекает сертификат или домен.",
  "target": "цель",
  "certificate": "сертификат",
  "checked": "проверено",
  "domains": "домены",
  "domain": "домен",
  "checks: {up} of {total} respond": "проверки: отвечают {up} из {total}",
  "site / port down": "сайт / порт не отвечает",
  "certificate expires in ≤": "сертификат истекает через ≤",
  "certificate expires in ≤ {n} days": "сертификат истекает через ≤ {n} дн.",
  "domain expires in ≤": "домен истекает через ≤",
  "domain expires in ≤ {n} days": "домен истекает через ≤ {n} дн.",
};

export const checksZh: Dict = {
  "{n} day ago|{n} days ago": "{n} 天前",
  "waiting for the first check": "等待首次检查",
  "port open": "端口开放",
  "down": "无法访问",
  "for {time}": "已 {time}",
  "latency": "延迟",
  "expiry unknown": "到期时间未知",
  "expiry unknown: {err}": "到期时间未知：{err}",
  "One target per line: https://site/path (status and certificate), tls://host:port (certificate), host:port (TCP port).":
    "每行一个目标：https://站点/路径（状态和证书）、tls://host:port（证书）、host:port（TCP 端口）。",
  "{n} down": "{n} 项无法访问",
  "all up": "全部正常",
  "checks": "检查",
  "No checks. Add the server's sites and ports: the panel checks them every minute and alerts when they are down, when a certificate or a domain is about to expire.":
    "暂无检查。添加服务器的网站和端口：面板每分钟检查一次，在无法访问、证书或域名即将到期时发出告警。",
  "target": "目标",
  "certificate": "证书",
  "checked": "检查于",
  "domains": "域名",
  "domain": "域名",
  "checks: {up} of {total} respond": "检查：{total} 项中 {up} 项正常",
  "site / port down": "网站 / 端口无法访问",
  "certificate expires in ≤": "证书到期 ≤",
  "certificate expires in ≤ {n} days": "证书 ≤ {n} 天内到期",
  "domain expires in ≤": "域名到期 ≤",
  "domain expires in ≤ {n} days": "域名 ≤ {n} 天内到期",
};

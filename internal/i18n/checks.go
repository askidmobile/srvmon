package i18n

// Translations of the HTTP/TCP/TLS checks and domain expiry (internal/checks),
// kept apart so the feature's strings sit together.
func init() {
	for k, v := range map[string][2]string{ // key → {ru, zh}
		"empty target":               {"пустая цель", "目标为空"},
		"%s: not a URL or host:port": {"%s: не URL и не host:port", "%s：不是 URL 也不是 host:port"},
		"%s: port from 1 to 65535":   {"%s: порт от 1 до 65535", "%s：端口应为 1 到 65535"},
		"%s: use http://, https://, tls:// or host:port": {
			"%s: используйте http://, https://, tls:// или host:port", "%s：请使用 http://、https://、tls:// 或 host:port"},
		"too many redirects":                  {"слишком много редиректов", "重定向次数过多"},
		"certificate expired":                 {"сертификат истёк", "证书已过期"},
		"invalid certificate: %s":             {"неверный сертификат: %s", "证书无效：%s"},
		"timeout":                             {"таймаут", "超时"},
		"host not found":                      {"хост не найден", "找不到主机"},
		"dns error":                           {"ошибка DNS", "DNS 错误"},
		"connection refused":                  {"соединение отклонено", "连接被拒绝"},
		"host unreachable":                    {"хост недоступен", "主机不可达"},
		"connection reset":                    {"соединение сброшено", "连接被重置"},
		"domain not found in the registry":    {"домен не найден в реестре", "注册局中找不到该域名"},
		"the zone has neither RDAP nor WHOIS": {"у зоны нет ни RDAP, ни WHOIS", "该域名后缀既没有 RDAP 也没有 WHOIS"},
		"%s is down: %s":                      {"%s не отвечает: %s", "%s 无法访问：%s"},
		"up to %d checks per server":          {"не больше %d проверок на сервер", "每台服务器最多 %d 项检查"},
		"threshold: from 0 to 120 days":       {"порог: от 0 до 120 дней", "阈值：0 到 120 天"},
		"Domains":                             {"Домены", "域名"},
		"certificate of %s expires in %d day (%s)|certificate of %s expires in %d days (%s)": {
			"сертификат %s истекает через %d день (%s)|сертификат %s истекает через %d дня (%s)|сертификат %s истекает через %d дней (%s)",
			"%s 的证书将在 %d 天后过期（%s）"},
		"certificate of %s expired %d day ago (%s)|certificate of %s expired %d days ago (%s)": {
			"сертификат %s истёк %d день назад (%s)|сертификат %s истёк %d дня назад (%s)|сертификат %s истёк %d дней назад (%s)",
			"%s 的证书已于 %d 天前过期（%s）"},
		"domain %s expires in %d day (%s)|domain %s expires in %d days (%s)": {
			"домен %s истекает через %d день (%s)|домен %s истекает через %d дня (%s)|домен %s истекает через %d дней (%s)",
			"域名 %s 将在 %d 天后到期（%s）"},
		"domain %s expired %d day ago (%s)|domain %s expired %d days ago (%s)": {
			"домен %s истёк %d день назад (%s)|домен %s истёк %d дня назад (%s)|домен %s истёк %d дней назад (%s)",
			"域名 %s 已于 %d 天前到期（%s）"},
	} {
		ru[k], zh[k] = v[0], v[1]
	}
}

package i18n

// Translations of sign-in with passkeys (internal/api/passkey.go), kept
// apart so the feature's strings sit together.
func init() {
	for k, v := range map[string][2]string{ // key → {ru, zh}
		"passkey": {"passkey", "通行密钥"},
		"a passkey %q for %s was added to the panel (IP %s)": {
			"в панель добавлен passkey %q для %s (IP %s)", "已添加通行密钥 %q（%s，IP %s）"},
		"the passkey %q for %s was removed from the panel (IP %s)": {
			"из панели удалён passkey %q для %s (IP %s)", "已删除通行密钥 %q（%s，IP %s）"},
	} {
		ru[k], zh[k] = v[0], v[1]
	}
}

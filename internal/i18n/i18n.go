// Package i18n translates the texts srvmon-server shows to people: Telegram
// messages, alerts, validation errors and setup logs.
//
// Source strings in the code are English fmt formats and double as keys;
// ru.go and zh.go map them to translations. A missing translation falls
// back to English. srvmon has one owner, so the language is one setting for
// the whole installation (store key "ui.lang"), held here as the current
// language.
//
// Adding a language: add a Lang constant, a dictionary file, list both in
// dicts and Langs, and add the same language in web/src/lib/i18n.ts.
package i18n

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// Lang is a language code.
type Lang string

// Supported languages.
const (
	EN Lang = "en"
	RU Lang = "ru"
	ZH Lang = "zh"
)

// Default is the language of a new installation.
const Default = EN

// SettingKey is the store setting that holds the language.
const SettingKey = "ui.lang"

// Langs lists the supported languages with their English names (the AI
// agent is told to reply in that language).
var Langs = []struct {
	Code Lang
	Name string
}{
	{EN, "English"},
	{RU, "Russian"},
	{ZH, "Simplified Chinese"},
}

var dicts = map[Lang]map[string]string{RU: ru, ZH: zh}

var current atomic.Value // Lang

// Parse returns the language for a code, and false for an unknown one.
func Parse(s string) (Lang, bool) {
	for _, l := range Langs {
		if string(l.Code) == s {
			return l.Code, true
		}
	}
	return "", false
}

// Set switches the current language; an unknown code is ignored.
func Set(code string) {
	if l, ok := Parse(code); ok {
		current.Store(l)
	}
}

// Current returns the current language.
func Current() Lang {
	if l, ok := current.Load().(Lang); ok {
		return l
	}
	return Default
}

// Name is the English name of a language ("Russian").
func Name(l Lang) string {
	for _, x := range Langs {
		if x.Code == l {
			return x.Name
		}
	}
	return "English"
}

func lookup(l Lang, s string) (string, bool) {
	tr, ok := dicts[l][s]
	return tr, ok
}

// T translates an English format into the current language and, with
// args, formats it like fmt.Sprintf.
func T(format string, args ...any) string {
	if tr, ok := lookup(Current(), format); ok {
		format = tr
	}
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}

// Errorf is fmt.Errorf with a translated format (%w works as usual).
func Errorf(format string, args ...any) error {
	if tr, ok := lookup(Current(), format); ok {
		format = tr
	}
	return fmt.Errorf(format, args...)
}

// N picks the plural form for n and formats it like T: N(n, "%d day",
// "%d days", n). The dictionary key is "one|other"; the value lists the
// language's forms separated by "|" (ru: one|few|many, zh: one form).
func N(n int, one, other string, args ...any) string {
	key := one + "|" + other
	l := Current()
	format := other
	if n == 1 || n == -1 {
		format = one
	}
	if tr, ok := lookup(l, key); ok {
		forms := strings.Split(tr, "|")
		i := 0
		switch l {
		case RU:
			i = ruForm(n)
		}
		if i >= len(forms) {
			i = len(forms) - 1
		}
		format = forms[i]
	}
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}

// ruForm: 0 for 1, 21, 31…; 1 for 2–4, 22–24…; 2 otherwise.
func ruForm(n int) int {
	if n < 0 {
		n = -n
	}
	m10, m100 := n%10, n%100
	switch {
	case m10 == 1 && m100 != 11:
		return 0
	case m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14):
		return 1
	}
	return 2
}

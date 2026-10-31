package i18n

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

var verb = regexp.MustCompile(`%(\[\d+\])?[-+# 0]*\d*(\.\d+)?[a-zA-Z%]`)

// Every translation must take the same arguments as its English format.
func TestDictionariesKeepVerbs(t *testing.T) {
	for l, d := range dicts {
		for k, v := range d {
			forms := []string{v}
			if strings.Contains(k, "|") {
				forms = strings.Split(v, "|")
			}
			want := verb.FindAllString(strings.Split(k, "|")[0], -1)
			for _, f := range forms {
				if got := verb.FindAllString(f, -1); !slices.Equal(got, want) {
					t.Errorf("%s %q: verbs %v, want %v", l, k, got, want)
				}
			}
		}
	}
}

func TestDictionariesHaveSameKeys(t *testing.T) {
	for k := range ru {
		if _, ok := zh[k]; !ok {
			t.Errorf("zh misses %q", k)
		}
	}
	for k := range zh {
		if _, ok := ru[k]; !ok {
			t.Errorf("ru misses %q", k)
		}
	}
}

func TestTranslate(t *testing.T) {
	defer Set(string(Default))
	if got := T("error: %s", "x"); got != "error: x" {
		t.Errorf("en: %q", got)
	}
	Set("ru")
	if Current() != RU {
		t.Fatalf("current = %s", Current())
	}
	if got := T("error: %s", "x"); got != "ошибка: x" {
		t.Errorf("ru: %q", got)
	}
	if got := T("no such string %d", 1); got != "no such string 1" {
		t.Errorf("fallback: %q", got)
	}
	Set("xx") // unknown: ignored
	if Current() != RU {
		t.Errorf("unknown code switched the language to %s", Current())
	}
}

func TestPlural(t *testing.T) {
	defer Set(string(Default))
	one, other := "container %s restarted %d time in %s", "container %s restarted %d times in %s"
	if got := N(1, one, other, "a", 1, "1h"); got != "container a restarted 1 time in 1h" {
		t.Errorf("en 1: %q", got)
	}
	if got := N(3, one, other, "a", 3, "1h"); got != "container a restarted 3 times in 1h" {
		t.Errorf("en 3: %q", got)
	}
	Set("ru")
	for n, want := range map[int]string{1: "раз", 2: "раза", 5: "раз", 12: "раз", 22: "раза", 21: "раз"} {
		if got := N(n, one, other, "a", n, "1ч"); !strings.Contains(got, " "+want+" за") {
			t.Errorf("ru %d: %q", n, got)
		}
	}
}

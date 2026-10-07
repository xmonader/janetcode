// Package i18n is the message catalogue for the interface's own words:
// help headings, the permission prompt, status labels and the common
// notices, in the terminal, the screen-reader line interface and the web
// page. It does not translate what the model writes (that is the
// `language` setting's instruction to the model), command names, config
// keys, or long reference text: in /help all only the everyday group's
// command descriptions are catalogued, and the rest stay English.
//
// A string is its own key: T("allow once") is the English text, or its
// translation when the active locale has one. A key the locale lacks is
// shown in English, so a partial catalogue is never a blank. The catalogue
// test holds every locale to the English key set and every T call in the
// tree to a key the catalogue has.
package i18n

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
)

// English is the key set: every string the interface passes through T,
// in English. A locale is a map from these to its own text.
var English = keys()

// locales are the catalogues other than English, by code.
var locales = map[string]map[string]string{
	"es": spanish,
	"fr": french,
}

// Locales are the catalogue codes the interface can be shown in, English
// first.
func Locales() []string {
	out := []string{"en"}
	for c := range locales {
		out = append(out, c)
	}
	sort.Strings(out[1:])
	return out
}

var active atomic.Value // string

// SetLanguage makes code the active locale; an unknown code is English.
func SetLanguage(code string) {
	if _, ok := locales[code]; !ok {
		code = "en"
	}
	active.Store(code)
}

// Active is the active locale's code.
func Active() string {
	if s, ok := active.Load().(string); ok {
		return s
	}
	return "en"
}

// T is s in the active locale, or s itself.
func T(s string) string { return In(Active(), s) }

// Tf is T of a format string, then formatted.
func Tf(format string, args ...any) string { return fmt.Sprintf(T(format), args...) }

// In is s in the named locale, or s itself.
func In(code, s string) string {
	if cat, ok := locales[code]; ok {
		if t, ok := cat[s]; ok && t != "" {
			return t
		}
	}
	return s
}

// Catalog is every key with its text in the named locale, for the web
// page to apply to its own elements.
func Catalog(code string) map[string]string {
	out := make(map[string]string, len(English))
	for k := range English {
		out[k] = In(code, k)
	}
	return out
}

// names are the spellings a `language` setting may use for a locale the
// catalogue has, lower-cased.
var names = map[string]string{
	"english": "en", "spanish": "es", "español": "es", "espanol": "es", "castellano": "es",
	"french": "fr", "français": "fr", "francais": "fr",
}

// Resolve picks the interface locale: from the `language` setting when it
// names one the catalogue has, else from LC_ALL, LC_MESSAGES or LANG, else
// English. A setting naming a language the catalogue lacks is English, not
// LANG: the person asked for a language, and the nearest thing to it the
// interface has is the default.
func Resolve(language string, getenv func(string) string) string {
	if language = strings.TrimSpace(language); language != "" {
		return code(language)
	}
	if getenv == nil {
		return "en"
	}
	for _, v := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if s := getenv(v); s != "" {
			return code(s)
		}
	}
	return "en"
}

// code maps "es", "es_MX.UTF-8", "pt-BR" or "Spanish" to a catalogue code,
// or "en".
func code(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if c, ok := names[s]; ok {
		return c
	}
	s, _, _ = strings.Cut(s, ".")
	s, _, _ = strings.Cut(s, "@")
	s = strings.ReplaceAll(s, "-", "_")
	s, _, _ = strings.Cut(s, "_")
	if _, ok := locales[s]; ok {
		return s
	}
	return "en"
}

func keys() map[string]bool {
	m := make(map[string]bool, len(source))
	for _, k := range source {
		m[k] = true
	}
	return m
}

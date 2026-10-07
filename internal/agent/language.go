package agent

import "strings"

// The language setting. The model is told, in the system prompt, which
// language to answer in; everything it writes for the project itself —
// code, identifiers, commit messages, file contents — stays in whatever
// the project already uses, because a translated identifier is a bug.

// SetLanguage sets the language the model answers in; empty leaves it to
// follow the user. Changing it changes the system prompt, so the next
// call pays for the prefix once.
func (a *Agent) SetLanguage(lang string) {
	a.settingsMu.Lock()
	a.language = strings.TrimSpace(lang)
	a.settingsMu.Unlock()
}

// Language is the language the model is told to answer in.
func (a *Agent) Language() string {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	return a.language
}

// languageSystem is the system prompt's paragraph for the setting, empty
// when there is none.
func (a *Agent) languageSystem() string {
	lang := a.Language()
	if lang == "" {
		return ""
	}
	return "\n\n# Language\nWrite every answer, explanation, question and plan for the user in " + lang +
		", whatever language the request or the files are in. Code, identifiers, commands, file paths, commit messages and file contents stay in the language the project already uses."
}

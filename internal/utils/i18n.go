package utils

// Tr returns Latin or Cyrillic text based on lang ("latn" or "cyrl").
// Default is Latin if lang is empty or unknown.
func Tr(lang, latn, cyrl string) string {
	if lang == "cyrl" {
		return cyrl
	}
	return latn
}


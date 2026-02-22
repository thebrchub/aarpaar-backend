package chat

import (
	"regexp"
	"strings"
)

// ---------------------------------------------------------------------------
// Text Sanitization
//
// StripHTMLTags removes all HTML tags from a string to prevent XSS.
// SanitizeMessage is the main entry point that strips tags and trims.
// ContainsProfanity does a basic word-boundary check for match mode.
// ---------------------------------------------------------------------------

var htmlTagRegex = regexp.MustCompile(`<[^>]*>`)

// StripHTMLTags removes all HTML/XML tags from the input string.
// Example: "<b>hello</b><script>alert(1)</script>" → "helloalert(1)"
func StripHTMLTags(s string) string {
	return htmlTagRegex.ReplaceAllString(s, "")
}

// SanitizeMessage strips HTML tags and trims whitespace.
// Returns empty string if the result is empty after sanitization.
func SanitizeMessage(s string) string {
	s = StripHTMLTags(s)
	s = strings.TrimSpace(s)
	return s
}

// profanityWords is a basic list for stranger match mode filtering.
// Expand as needed. All entries must be lowercase.
var profanityWords = []string{
	"fuck", "shit", "bitch", "asshole", "dick", "pussy",
	"nigger", "faggot", "retard", "cunt", "whore", "slut",
}

// profanityRegexes are compiled once at startup for performance.
var profanityRegexes []*regexp.Regexp

func init() {
	for _, word := range profanityWords {
		// \b ensures we match whole words, not substrings
		pattern := `(?i)\b` + regexp.QuoteMeta(word) + `\b`
		profanityRegexes = append(profanityRegexes, regexp.MustCompile(pattern))
	}
}

// ContainsProfanity checks if the text contains any profane words.
// Used only for stranger match rooms, not DM/channel messages.
func ContainsProfanity(s string) bool {
	lower := strings.ToLower(s)
	for _, re := range profanityRegexes {
		if re.MatchString(lower) {
			return true
		}
	}
	return false
}

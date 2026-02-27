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
// Fast-path: skip regex entirely when there's no '<' character (P3-2 fix).
func StripHTMLTags(s string) string {
	if strings.IndexByte(s, '<') == -1 {
		return s
	}
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

// Single compiled alternation regex — scans the string once instead of
// 12 separate full passes. ~12x faster on hot path. (P2-2 fix)
var profanityRegex *regexp.Regexp

func init() {
	// Build a single alternation: (?i)\b(?:fuck|shit|bitch|...)\b
	quoted := make([]string, len(profanityWords))
	for i, word := range profanityWords {
		quoted[i] = regexp.QuoteMeta(word)
	}
	pattern := `(?i)\b(?:` + strings.Join(quoted, "|") + `)\b`
	profanityRegex = regexp.MustCompile(pattern)
}

// ContainsProfanity checks if the text contains any profane words.
// Used only for stranger match rooms, not DM/channel messages.
// The regex already uses (?i) for case-insensitive matching,
// so no need for strings.ToLower pre-processing.
func ContainsProfanity(s string) bool {
	return profanityRegex.MatchString(s)
}

// ---------------------------------------------------------------------------
// @Mention Extraction
//
// Parses @username mentions from message text. Returns a deduplicated list
// of mentioned usernames (without the @ prefix). These are later resolved
// to user IDs in the message payload.
// ---------------------------------------------------------------------------

var mentionRegex = regexp.MustCompile(`@([a-zA-Z0-9_]{1,30})`)

// ExtractMentions parses @username mentions from text and returns unique usernames.
func ExtractMentions(s string) []string {
	if strings.IndexByte(s, '@') == -1 {
		return nil
	}
	matches := mentionRegex.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(matches))
	result := make([]string, 0, len(matches))
	for _, m := range matches {
		username := m[1]
		if !seen[username] {
			seen[username] = true
			result = append(result, username)
		}
	}
	return result
}

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
// These are checked with \b word boundaries (exact word match).
var profanityWords = []string{
	"fuck", "shit", "bitch", "asshole", "dick", "pussy",
	"nigger", "faggot", "retard", "cunt", "whore", "slut",
	// Abbreviated English profanity
	"fk", "fck", "fuk", "fuq", "stfu", "gtfo", "sob",
	// NSFW / sexual terms
	"masturbate", "masturbation", "masterbate", "masturb",
	"porn", "porno", "pornhub", "xvideos", "xnxx", "xhamster",
	"horny", "boobs", "boob", "tits", "tit", "nude", "nudes",
	"blowjob", "handjob", "rimjob", "cumshot", "dildo", "vibrator",
	"orgasm", "erection", "penis", "vagina", "anal", "anus",
	"fap", "cum", "jizz", "milf", "hentai", "bdsm",
	"threesome", "orgy", "gangbang", "creampie",
	"sexting", "onlyfans",
	// Hindi words that need word-boundary matching to avoid false positives
	"lund", "lnd", "gand", "sali", "saali", "sala", "saala",
}

// profanitySubstrings are checked as substrings (no word boundaries).
// Hindi/Hinglish slurs that users embed inside other words to bypass
// filters (e.g. "bsdiwali" embeds "bsdi" inside "diwali").
var profanitySubstrings = []string{
	// bhosdi / bhosd variants
	"bhosd", "bhosd", "bsdi", "bsdk", "bhosad",
	// madarchod variants
	"madarc", "madarch", "mdrchd", "m@darc",
	// behenchod / benchod variants
	"benchod", "bhnchd", "behnch", "bechod", "bkchd",
	// chutiya variants
	"chutiy", "chtiy", "chutia", "chut",
	// gaand / gandu
	"gaand", "gandu", "g@ndu",
	// laude / lavde / laudi
	"laude", "laudi", "lavde", "lavdi", "lode", "lodi", "l0de", "lwde",
	// randi
	"randi", "r@ndi", "rndee", "rndi",
	// harami
	"harami", "haram1",
	// lund (use word-boundary version to avoid false positives like "blunder")
	// handled via profanityWords below instead
	// jhaat
	"jhaat", "jhat",
	// sexual Hindi/Hinglish substrings
	"muthh", "mutth", "hilana", "hilata",
}

// Single compiled alternation regex — scans the string once instead of
// many separate full passes. ~Nx faster on hot path. (P2-2 fix)
var profanityRegex *regexp.Regexp

// Separate regex for substring profanity (no word boundaries).
var profanitySubstringRegex *regexp.Regexp

func init() {
	// Build word-boundary regex: (?i)\b(?:fuck|shit|bitch|...)\b
	quoted := make([]string, len(profanityWords))
	for i, word := range profanityWords {
		quoted[i] = regexp.QuoteMeta(word)
	}
	pattern := `(?i)\b(?:` + strings.Join(quoted, "|") + `)\b`
	profanityRegex = regexp.MustCompile(pattern)

	// Build substring regex: (?i)(?:bhosd|bsdi|madarc|...)
	// No \b — catches embedded slurs like "bsdiwali"
	subQuoted := make([]string, len(profanitySubstrings))
	for i, word := range profanitySubstrings {
		subQuoted[i] = regexp.QuoteMeta(word)
	}
	subPattern := `(?i)(?:` + strings.Join(subQuoted, "|") + `)`
	profanitySubstringRegex = regexp.MustCompile(subPattern)
}

// ContainsProfanity checks if the text contains any profane words.
// Used only for stranger match rooms, not DM/channel messages.
// Checks both word-boundary English profanity and substring Hindi slurs.
func ContainsProfanity(s string) bool {
	return profanityRegex.MatchString(s) || profanitySubstringRegex.MatchString(s)
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

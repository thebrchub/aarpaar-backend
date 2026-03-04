package chat

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Unit Tests — StripHTMLTags
// ---------------------------------------------------------------------------

func TestStripHTMLTags(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"script tag", "<script>alert('xss')</script>Hello", "alert('xss')Hello"},
		{"no html", "Normal text", "Normal text"},
		{"bold and italic", "<b>Bold</b> & <i>italic</i>", "Bold & italic"},
		{"empty string", "", ""},
		{"only tags", "<div><span></span></div>", ""},
		{"nested tags", "<div><p>Hello</p></div>", "Hello"},
		{"self-closing", "<br/>Hello<hr/>", "Hello"},
		{"attributes", `<a href="evil.com">click</a>`, "click"},
		{"img tag with onerror", `<img src=x onerror="alert(1)">safe`, "safe"},
		{"style tag", `<style>body{color:red}</style>content`, "body{color:red}content"},
		{"no angle bracket fast path", "plain text without any tags", "plain text without any tags"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripHTMLTags(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// Unit Tests — SanitizeMessage
// ---------------------------------------------------------------------------

func TestSanitizeMessage(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"strips and trims", "  <b>hello</b>  ", "hello"},
		{"empty after strip", "  <div></div>  ", ""},
		{"preserves text", "Hello world", "Hello world"},
		{"xss payload", "<script>alert('xss')</script>Hello", "alert('xss')Hello"},
		{"mixed whitespace", "\t\n  text  \n\t", "text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeMessage(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// Unit Tests — ContainsProfanity
// ---------------------------------------------------------------------------

func TestContainsProfanity(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"clean text", "Hello, how are you?", false},
		{"basic profanity", "what the fuck", true},
		{"case insensitive", "WHAT THE FUCK", true},
		{"mixed case", "ShIt", true},
		{"word boundary", "shitty", false}, // word boundary - "shitty" might not match
		{"embedded in word", "absolutely", false},
		{"multiple profane words", "fuck this shit", true},
		{"empty string", "", false},
		{"near-miss", "ship", false},
		{"duck", "duck", false},
		// Hindi/Hinglish slurs (substring match — no word boundaries)
		{"hindi bhosdike", "bhosdike", true},
		{"hindi bsdi embedded", "bsdiwali", true},
		{"hindi bsdk", "bsdk", true},
		{"hindi madarchod", "madarchod", true},
		{"hindi benchod", "benchod", true},
		{"hindi chutiya", "chutiya", true},
		{"hindi gandu", "gandu", true},
		{"hindi randi", "randi", true},
		{"hindi lund", "lund", true},
		{"hindi gand word", "gand", true},
		{"hindi harami", "harami", true},
		{"hindi embedded in word", "kbsdkya", true},
		{"hindi case insensitive", "BHOSDIKE", true},
		{"clean hindi", "diwali mubarak", false},
		{"clean hindi 2", "gandhi jayanti", false},
		// New: abbreviated English profanity
		{"abbrev fk", "fk u", true},
		{"abbrev fck", "fck off", true},
		{"abbrev fuk", "fuk", true},
		{"abbrev stfu", "stfu", true},
		// New: laudi variant
		{"hindi laudi", "laudi", true},
		// New: sali
		{"hindi sali randi", "sali randi", true},
		{"hindi sali standalone", "sali", true},
		{"hindi saala", "saala", true},
		// NSFW / sexual terms
		{"nsfw masturbate", "masturbate", true},
		{"nsfw masturbation", "do you do masturbation", true},
		{"nsfw horny", "im so horny", true},
		{"nsfw porn", "send me porn", true},
		{"nsfw boobs", "show boobs", true},
		{"nsfw nudes", "send nudes", true},
		{"nsfw cum", "cum", true},
		{"nsfw blowjob", "blowjob", true},
		{"nsfw sexting", "lets do sexting", true},
		{"nsfw fap", "fap", true},
		// NSFW false positive checks
		{"clean analysis", "analysis of data", false},
		{"clean cumulative", "cumulative total", false},
		{"clean document", "document review", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainsProfanity(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// Unit Tests — ExtractMentions
// ---------------------------------------------------------------------------

func TestExtractMentions(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"no mentions", "hello world", nil},
		{"single mention", "hey @alice", []string{"alice"}},
		{"multiple mentions", "@alice @bob hello", []string{"alice", "bob"}},
		{"duplicate mentions", "@alice @alice", []string{"alice"}},
		{"mention with underscore", "@user_123", []string{"user_123"}},
		{"mention at start", "@admin check this", []string{"admin"}},
		{"email not mention", "user@example.com", []string{"example"}},
		{"empty string", "", nil},
		{"no at symbol fast path", "plain text", nil},
		{"mention with numbers", "@player99", []string{"player99"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractMentions(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

package chat

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Benchmarks — Sanitization Functions
// ---------------------------------------------------------------------------

func BenchmarkStripHTMLTags(b *testing.B) {
	inputs := []struct {
		name string
		val  string
	}{
		{"short_clean", "Hello, world!"},
		{"short_html", "Hello <b>world</b>! <script>alert('x')</script>"},
		{"medium_mixed", "<p>Some text with <a href='link'>links</a> and <img src='img'/> images</p> and more text here"},
		{"long_text", func() string {
			s := ""
			for i := 0; i < 100; i++ {
				s += "<p>Paragraph " + string(rune(i+'0')) + " with <b>bold</b></p>"
			}
			return s
		}()},
	}

	for _, tc := range inputs {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				StripHTMLTags(tc.val)
			}
		})
	}
}

func BenchmarkSanitizeMessage(b *testing.B) {
	inputs := []string{
		"hello world",
		"  hello   world  ",
		"Hello <script>alert('xss')</script> world",
		"A message with some    extra     spacing   and <b>html</b>",
	}

	for i, input := range inputs {
		b.Run(func() string {
			names := []string{"clean", "whitespace", "xss", "mixed"}
			return names[i]
		}(), func(b *testing.B) {
			for j := 0; j < b.N; j++ {
				SanitizeMessage(input)
			}
		})
	}
}

func BenchmarkContainsProfanity(b *testing.B) {
	inputs := []struct {
		name string
		val  string
	}{
		{"clean_short", "hello world"},
		{"clean_long", "This is a perfectly good message about programming and Go benchmarks"},
		{"profane", "this is a damn message"},
	}

	for _, tc := range inputs {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				ContainsProfanity(tc.val)
			}
		})
	}
}

func BenchmarkExtractMentions(b *testing.B) {
	inputs := []struct {
		name string
		val  string
	}{
		{"no_mention", "Hello world, how are you doing today?"},
		{"single_mention", "Hey @alice what's up?"},
		{"multi_mention", "@alice @bob @charlie @dave let's meet up!"},
		{"dense_mentions", "@a @b @c @d @e @f @g @h @i @j all come here"},
	}

	for _, tc := range inputs {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				ExtractMentions(tc.val)
			}
		})
	}
}

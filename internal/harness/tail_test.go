//go:build !windows

package harness

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTailUTF8(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		limit int
		want  string
	}{
		{"empty", "", 3, ""},
		{"ascii", "abcdef", 3, "def"},
		{"within limit", "日本語", 10, "日本語"},
		{"exact limit", "日本語", 9, "日本語"},
		{"character boundary", "日本語", 6, "本語"},
		{"first continuation byte", "日本語", 5, "語"},
		{"second continuation byte", "日本語", 4, "語"},
		{"last character does not fit", "日本語", 2, ""},
		{"mixed ascii suffix", "日本語ab", 4, "ab"},
		{"four byte character", "日本🙂", 5, "🙂"},
		{"four byte character does not fit", "日本🙂", 3, ""},
		{"zero limit", "日本語", 0, ""},
		{"negative limit", "日本語", -1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tail(tt.text, tt.limit)
			if got != tt.want {
				t.Errorf("tail(%q, %d) = %q, want %q", tt.text, tt.limit, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("tail returned invalid UTF-8: %q", got)
			}
		})
	}
}

func TestTailByteLimits(t *testing.T) {
	for _, text := range []string{"", "abcdef", "日本語", "aé日本🙂z", "日本語\n停止の理由です。"} {
		for limit := 0; limit <= len(text)+1; limit++ {
			got := tail(text, limit)
			if !utf8.ValidString(got) || len(got) > limit || !strings.HasSuffix(text, got) {
				t.Errorf("tail(%q, %d) = %q: invalid UTF-8, exceeds limit, or is not a suffix", text, limit, got)
			}
			if len(text) <= limit && got != text {
				t.Errorf("tail(%q, %d) = %q: input within limit changed", text, limit, got)
			}
		}
	}
}

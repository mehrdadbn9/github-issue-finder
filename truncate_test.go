package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// truncateString used to slice by byte. Any multi-byte character straddling the
// cut was left as a partial rune, and Telegram rejects that outright: one cycle
// lost the alerts for cilium#47834 and k3s#14501 to "Bad Request: text must be
// encoded in UTF-8" while still logging the batch as sent.
func TestTruncateStringKeepsValidUTF8(t *testing.T) {
	titles := []string{
		// An em dash is 3 bytes, so cutting at a byte boundary splits it.
		"Kubernetes v1.35.1 bundles CoreDNS v1.13.1 — 3 minor versions behind upstream v1.14.6",
		"Volume API and index stats over-report after log deletion — queries stay correct",
		"CUDA illegal memory access in ggml_cuda_flash_attn_ext_mma_f16_case<256, 256, …>",
		"Проверка юникода в заголовке задачи, который довольно длинный",
		"日本語のタイトルはマルチバイト文字で構成されている",
		"emoji in the title 🐛🔥 and then a lot more text to push past the limit",
	}

	for _, title := range titles {
		for _, n := range []int{1, 2, 3, 4, 10, 40, 79, 80, 81} {
			got := truncateString(title, n)
			if !utf8.ValidString(got) {
				t.Errorf("truncateString(%q, %d) produced invalid UTF-8: %q", title, n, got)
			}
			if utf8.RuneCountInString(got) > n {
				t.Errorf("truncateString(%q, %d) returned %d runes, want at most %d",
					title, n, utf8.RuneCountInString(got), n)
			}
		}
	}
}

// Short strings must come back untouched, and the ellipsis must only appear when
// something was actually cut.
func TestTruncateStringBoundaries(t *testing.T) {
	tests := []struct {
		in     string
		maxLen int
		want   string
	}{
		{"short", 80, "short"},
		{"exactly ten", 11, "exactly ten"},
		{"abcdefghij", 8, "abcde..."},
		{"日本語のタイトル", 5, "日本..."},
		{"anything", 0, ""},
		{"anything", -1, ""},
	}

	for _, tt := range tests {
		if got := truncateString(tt.in, tt.maxLen); got != tt.want {
			t.Errorf("truncateString(%q, %d) = %q, want %q", tt.in, tt.maxLen, got, tt.want)
		}
	}
}

// A multi-byte string that fits by rune count but not by byte count must be
// returned whole - the old byte comparison truncated it for no reason.
func TestTruncateStringCountsRunesNotBytes(t *testing.T) {
	title := strings.Repeat("é", 50) // 50 runes, 100 bytes
	if got := truncateString(title, 80); got != title {
		t.Errorf("a 50-rune title was truncated at maxLen 80: got %d runes", utf8.RuneCountInString(got))
	}
}

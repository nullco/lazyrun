package gui

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func checkSafe(t testing.TB, text string) {
	t.Helper()
	if !utf8.ValidString(text) {
		t.Fatal("invalid UTF-8 reached rendering")
	}
	for len(text) > 0 {
		if strings.HasPrefix(text, "\x1b[") {
			i := strings.IndexByte(text, 'm')
			if i < 0 || i > 130 || !validSGR(text[2:i]) {
				t.Fatalf("unsafe escape: %q", text)
			}
			text = text[i+1:]
			continue
		}
		r, n := utf8.DecodeRuneInString(text)
		if r != '\n' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
			t.Fatalf("unsafe control %U", r)
		}
		text = text[n:]
	}
}
func TestSanitizerStreamingAndAllowlist(t *testing.T) {
	input := "α\x1b[31mRED\x1b[0m\r\nnext\x00\a\b\x1b[2J\x1b[1;2H" +
		"\x1b]52;c;clipboard\aOK\x1b]0;title\x1b\\" +
		"\x1bPprivate\x1b\\\x1b]8;;https://bad\x1b\\link\x1b]8;;\x1b\\" +
		"\u009dsecret\u009c\u202eevil\xff\xe2\x82\xac\tend"
	want := "α\x1b[31mRED\x1b[0m\nnextOKlinkevil�€    end"
	for _, chunk := range []int{1, 2, 7, len(input)} {
		var s Sanitizer
		var got strings.Builder
		for i := 0; i < len(input); i += chunk {
			got.WriteString(s.Feed([]byte(input[i:min(i+chunk, len(input))])))
		}
		got.WriteString(s.Finish())
		checkSafe(t, got.String())
		if got.String() != want {
			t.Fatalf("chunk %d: %q", chunk, got.String())
		}
	}
	for _, input := range []string{"\x1b[38;2;255;0;12mcolor", "\x1b[48;5;240mcolor", "\x1b[mreset"} {
		var s Sanitizer
		got := s.Feed([]byte(input))
		if got != input {
			t.Fatal(got)
		}
		checkSafe(t, got)
	}
}
func TestSanitizerBoundsAndIncompleteFinalData(t *testing.T) {
	var s Sanitizer
	input := "\x1b[" + strings.Repeat("1;", 100000) + "mSAFE"
	if got := s.Feed([]byte(input)); got != "SAFE" || len(s.csi) > 128 {
		t.Fatal(len(got), len(s.csi))
	}
	s.Reset()
	s.Feed([]byte("\x1b]" + strings.Repeat("x", 2*MaxBufferBytes)))
	if len(s.csi) > 128 || len(s.pending) > 3 {
		t.Fatal("unbounded parser carry")
	}
	if got := s.Feed([]byte("\aVISIBLE")); got != "VISIBLE" {
		t.Fatal(got)
	}
	s.Reset()
	if s.Feed([]byte{0xe2, 0x82}) != "" || s.Finish() != "�" {
		t.Fatal("lost incomplete UTF-8")
	}
	s.Reset()
	s.Feed([]byte("\x1b[31"))
	if s.Finish() != "" || s.Feed([]byte("ok")) != "ok" {
		t.Fatal("dangling escape")
	}
	if got := singleLine("bad\x1b]0;title\a\x1b[31mname\npath"); got != "badname path" {
		t.Fatal(got)
	}
	for _, params := range []string{"38", "38;2;256;0;0", "38;5", "1;;31", "999", strings.Repeat("1;", 22) + "0"} {
		if validSGR(params) {
			t.Fatal(params)
		}
	}
}
func FuzzSanitizer(f *testing.F) {
	for _, input := range []string{"hello", "\x1b[31mred\x1b[0m", "\x1b]52;c;bad\a", "\x1b[38m", "\xff\xe2"} {
		f.Add([]byte(input), uint8(3))
	}
	f.Fuzz(func(t *testing.T, input []byte, size uint8) {
		var s Sanitizer
		var out strings.Builder
		chunk := int(size) + 1
		for i := 0; i < len(input); i += chunk {
			out.WriteString(s.Feed(input[i:min(i+chunk, len(input))]))
			if len(s.pending) > 3 || len(s.csi) > 128 {
				t.Fatal("unbounded carry")
			}
		}
		out.WriteString(s.Finish())
		checkSafe(t, out.String())
		checkSafe(t, plain(string(input)))
	})
}

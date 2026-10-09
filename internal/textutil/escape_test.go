package textutil

import "testing"

func TestEscapeControls(t *testing.T) {
	got := EscapeControls("normal α\n\t\x1b]52;c;bad\a\r\b\u202e\u009b\xff")
	want := "normal α\n\t\\x1b]52;c;bad\\a\\r\\b\\u202e\\u009b�"
	if got != want {
		t.Fatalf("%q != %q", got, want)
	}
}

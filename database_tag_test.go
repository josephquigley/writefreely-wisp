package writefreely

import (
	"regexp"
	"strings"
	"testing"
)

// TestTagRegexpTermIsLiteral checks the pattern piece the tag queries build
// from a URL tag: it must compile, and match the tag and nothing else.
func TestTagRegexpTermIsLiteral(t *testing.T) {
	for _, tag := range []string{"g.x", "a(b", "C++", "x|y", "[a-z]", `back\slash`, "^$", "{1}", "Go"} {
		re, err := regexp.Compile("^#" + tagRegexpTerm(tag) + "$")
		if err != nil {
			t.Errorf("tag %q: pattern does not compile: %v", tag, err)
			continue
		}
		lower := "#" + strings.ToLower(tag)
		if !re.MatchString(lower) {
			t.Errorf("tag %q: pattern %q does not match %q", tag, re, lower)
		}
	}
	if regexp.MustCompile("#" + tagRegexpTerm("g.x")).MatchString("#gox") {
		t.Errorf(`tag "g.x" matched "#gox": "." was not escaped`)
	}
}

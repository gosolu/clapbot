package web

import (
	"strings"
	"testing"
)

func TestHTMLToMarkdown(t *testing.T) {
	raw := `<html><head><title>Ignored</title><script>alert(1)</script></head><body><h1>Hello World</h1><p>Visit <a href="https://example.com">Example</a></p><ul><li>Item 1</li><li>Item 2</li></ul></body></html>`
	md := HTMLToMarkdown(raw)
	if !strings.Contains(md, "# Hello World") {
		t.Errorf("missing heading in %q", md)
	}
	if !strings.Contains(md, "[Example](https://example.com)") {
		t.Errorf("missing link in %q", md)
	}
	if !strings.Contains(md, "- Item 1") {
		t.Errorf("missing list item in %q", md)
	}
}

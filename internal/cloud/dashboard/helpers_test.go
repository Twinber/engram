package dashboard

import (
	"strings"
	"testing"
)

// TestSafeQueryDefangsPercentEncodedQuoteAttack is the RED test for N1.
// html.EscapeString on a rawQuery containing '&' turns it into '&amp;', which
// is wrong inside an href attribute — the browser sees &amp; literally and the
// URL breaks. The fix normalises the query via url.ParseQuery + Encode, which
// produces correct percent-encoded output without HTML-entity pollution.
func TestSafeQueryDefangsPercentEncodedQuoteAttack(t *testing.T) {
	// Multi-param query: old html.EscapeString turns & into &amp; inside href.
	result := safeQuery("/dashboard/admin/audit-log/list", `contributor=alice&page=2`)
	// The result must NOT contain &amp; — that would break the URL in href.
	if strings.Contains(result, "&amp;") {
		t.Errorf("safeQuery HTML-escaped '&' in URL: %q (must not contain &amp;)", result)
	}
	// The result must still be a valid URL (starts with the path).
	if !strings.HasPrefix(result, "/dashboard/admin/audit-log/list") {
		t.Errorf("safeQuery dropped or mangled the path: %q", result)
	}
}

// TestSafeQueryRejectsLiteralDoubleQuote ensures a rawQuery with a literal
// double-quote does NOT appear unescaped in the output (XSS breakout via href).
func TestSafeQueryRejectsLiteralDoubleQuote(t *testing.T) {
	// Literal " in a rawQuery is a malformed query string. url.ParseQuery will
	// URL-encode it to %22, preventing it from breaking an HTML attribute boundary.
	result := safeQuery("/path", `q="onmouseover=alert(1)`)
	if strings.Contains(result, `"`) {
		t.Errorf("safeQuery returned unescaped double-quote in URL: %q", result)
	}
}

// TestSafeQueryPreservesNormalParams verifies that well-formed params round-trip correctly.
func TestSafeQueryPreservesNormalParams(t *testing.T) {
	result := safeQuery("/dashboard/admin/audit-log/list", "contributor=alice&page=2")
	if !strings.Contains(result, "contributor=alice") {
		t.Errorf("safeQuery dropped contributor param: %q", result)
	}
	if !strings.Contains(result, "page=2") {
		t.Errorf("safeQuery dropped page param: %q", result)
	}
}

// TestSafeQueryEmptyRawQueryReturnsPath verifies no trailing '?' is appended.
func TestSafeQueryEmptyRawQueryReturnsPath(t *testing.T) {
	result := safeQuery("/dashboard/projects", "")
	if result != "/dashboard/projects" {
		t.Errorf("safeQuery with empty rawQuery = %q, want %q", result, "/dashboard/projects")
	}
}

func TestSanitizeDashboardNextNormalizesAndConstrainsPath(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty next", raw: "", want: ""},
		{name: "simple dashboard path", raw: "/dashboard/projects?q=alpha", want: "/dashboard/projects?q=alpha"},
		{name: "dot segment stays in dashboard namespace", raw: "/dashboard/projects/../admin", want: "/dashboard/admin"},
		{name: "encoded dot segment stays in dashboard namespace", raw: "/dashboard/projects/%2e%2e?q=beta", want: "/dashboard?q=beta"},
		{name: "dot segment escaping dashboard rejected", raw: "/dashboard/../admin", want: ""},
		{name: "encoded dot segment escaping dashboard rejected", raw: "/dashboard/%2e%2e/admin", want: ""},
		{name: "dashboard prefix must be exact namespace", raw: "/dashboarding", want: ""},
		{name: "absolute URL rejected", raw: "https://evil.example/dashboard", want: ""},
		{name: "scheme-relative URL rejected", raw: "//evil.example/dashboard", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeDashboardNext(tt.raw); got != tt.want {
				t.Fatalf("sanitizeDashboardNext(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

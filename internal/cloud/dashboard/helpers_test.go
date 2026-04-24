package dashboard

import "testing"

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

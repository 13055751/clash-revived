package main

import "testing"

func TestPkgFromPath(t *testing.T) {
	cases := map[string]string{
		"/data/app/~~sfsx9WbCRH77GfPx80Pc_Q==/com.github.kr328.clash-OWTdxIzEzNkhgrQJPp3gug==/lib/arm64/libclash.so": "com.github.kr328.clash",
		"/data/user/0/com.github.kr328.clash/cache/x.log":                                                            "com.github.kr328.clash",
		"/data/app/com.example.app-abc==/lib/arm64/libclash.so":                                                      "com.example.app",
		"": "",
	}
	for in, want := range cases {
		if got := pkgFromPath(in); got != want {
			t.Errorf("pkgFromPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizePkg(t *testing.T) {
	good := []string{"com.github.kr328.clash", "a.b", "com.x.y_z"}
	bad := []string{"", "/data/app", "Com.Foo", "nodot", "a/b"}
	for _, s := range good {
		if sanitizePkg(s) == "" {
			t.Errorf("should accept %q", s)
		}
	}
	for _, s := range bad {
		if sanitizePkg(s) != "" {
			t.Errorf("should reject %q, got %q", s, sanitizePkg(s))
		}
	}
}

func TestSanitizePkgStripsProcessSuffix(t *testing.T) {
	if got := sanitizePkg("com.github.kr328.clash:core"); got != "com.github.kr328.clash" {
		t.Errorf("got %q", got)
	}
}

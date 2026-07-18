package model

import "testing"

func TestParseCanonicalURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"HTTPS://Example.COM:8080", "http://example.com:8080"},
		{"socks://user:p%40ss@127.0.0.1:1080", "socks5://user:p%40ss@127.0.0.1:1080"},
		{"socks5://u%20ser:pass@[2001:0db8::1]:1080", "socks5://u%20ser:pass@[2001:db8::1]:1080"},
	}
	for _, test := range tests {
		proxy, err := Parse(test.input)
		if err != nil {
			t.Fatalf("Parse(%q): %v", test.input, err)
		}
		if got := proxy.URL(); got != test.want {
			t.Fatalf("Parse(%q).URL()=%q want=%q", test.input, got, test.want)
		}
	}
}

func TestParseRejectsUnsupportedOrAmbiguousURLs(t *testing.T) {
	for _, input := range []string{
		"vmess://example.com:443",
		"http://example.com",
		"http://example.com:80/path",
		"http://example.com:80?x=1",
		"http://bad host:80",
		"http://bad:host:80",
	} {
		if _, err := Parse(input); err == nil {
			t.Fatalf("Parse(%q) unexpectedly succeeded", input)
		}
	}
}

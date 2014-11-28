package main

import (
	"net/http"
	"testing"
)

func TestDomain(t *testing.T) {
	for i, tt := range []struct {
		host, cookied, sub, domain string
	}{
		{"internal.example.com", "<default>", "", "internal.example.com"},
		{"internal.example.com", ".example.com", "internal", ".example.com"},
		{"foo.internal.example.com", ".example.com", "foo.internal", ".example.com"},
	} {
		req := &http.Request{Host: tt.host}
		p := OauthProxy{CookieDomain: tt.cookied}

		if gsub, gdomain := p.Domain(req); gsub != tt.sub || gdomain != tt.domain {
			t.Errorf("%d: got %s, %s; want %s, %s", i, gsub, gdomain, tt.sub, tt.domain)
		}
	}
}

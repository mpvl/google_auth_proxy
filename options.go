package main

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Configuration Options that can be set by Command Line Flag, or Config File
type Options struct {
	HttpAddress             string        `flag:"http-address" cfg:"http_address"`
	RedirectUrl             string        `flag:"redirect-url" cfg:"redirect_url"`
	ClientID                string        `flag:"client-id" cfg:"client_id" env:"GOOGLE_AUTH_PROXY_CLIENT_ID"`
	ClientSecret            string        `flag:"client-secret" cfg:"client_secret" env:"GOOGLE_AUTH_PROXY_CLIENT_SECRET"`
	PassBasicAuth           bool          `flag:"pass-basic-auth" cfg:"pass_basic_auth"`
	HtpasswdFile            string        `flag:"htpasswd-file" cfg:"htpasswd_file"`
	CookieSecret            string        `flag:"cookie-secret" cfg:"cookie_secret" env:"GOOGLE_AUTH_PROXY_COOKIE_SECRET"`
	CookieDomain            string        `flag:"cookie-domain" cfg:"cookie_domain" env:"GOOGLE_AUTH_PROXY_COOKIE_DOMAIN"`
	CookieExpire            time.Duration `flag:"cookie-expire" cfg:"cookie_expire" env:"GOOGLE_AUTH_PROXY_COOKIE_EXPIRE"`
	CookieHttpsOnly         bool          `flag:"cookie-https-only" cfg:"cookie_https_only"`
	AuthenticatedEmailsFile string        `flag:"authenticated-emails-file" cfg:"authenticated_emails_file"`
	GoogleAppsDomains       []string      `flag:"google-apps-domain" cfg:"google_apps_domains"`
	Upstreams               []string      `flag:"upstream" cfg:"upstreams"`

	// internal values that are set after config validation
	redirectUrl *url.URL
	proxyUrls   map[string][]*url.URL
}

func NewOptions() *Options {
	return &Options{
		HttpAddress:     "127.0.0.1:4180",
		CookieHttpsOnly: true,
		PassBasicAuth:   true,
		CookieExpire:    time.Duration(168) * time.Hour,
	}
}

func (o *Options) Validate() error {
	if len(o.Upstreams) < 1 {
		return errors.New("missing setting: upstream")
	}
	if o.CookieSecret == "" {
		errors.New("missing setting: cookie-secret")
	}
	if o.ClientID == "" {
		return errors.New("missing setting: client-id")
	}
	if o.ClientSecret == "" {
		return errors.New("missing setting: client-secret")
	}

	redirectUrl, err := url.Parse(o.RedirectUrl)
	if err != nil {
		return fmt.Errorf("error parsing redirect-url=%q %s", o.RedirectUrl, err)
	}
	o.redirectUrl = redirectUrl

	o.proxyUrls = map[string][]*url.URL{}
	for _, u := range o.Upstreams {
		var sub string
		if i := strings.IndexByte(u, '|'); i != -1 {
			sub, u = u[:i], u[i+1:]
		}
		upstreamUrl, err := url.Parse(u)
		if err != nil {
			return fmt.Errorf("error parsing upstream=%q %s", upstreamUrl, err)
		}
		if upstreamUrl.Path == "" {
			upstreamUrl.Path = "/"
		}
		o.proxyUrls[sub] = append(o.proxyUrls[sub], upstreamUrl)
	}

	return nil
}

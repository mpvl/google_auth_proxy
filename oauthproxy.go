package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/bitly/go-simplejson"
)

const pingPath = "/ping"
const signInPath = "/oauth2/sign_in"
const oauthStartPath = "/oauth2/start"
const oauthCallbackPath = "/oauth2/callback"

type OauthProxy struct {
	CookieSeed      string
	CookieKey       string
	CookieDomain    string
	CookieHttpsOnly bool
	CookieExpire    time.Duration
	Validator       func(string) bool

	redirectUrl        *url.URL // the url to receive requests at
	oauthRedemptionUrl *url.URL // endpoint to redeem the code
	oauthLoginUrl      *url.URL // to redirect the user to
	oauthScope         string
	clientID           string
	clientSecret       string
	SignInMessage      string
	HtpasswdFile       *HtpasswdFile
	serveMuxes         map[string]*http.ServeMux
	PassBasicAuth      bool
}

func NewOauthProxy(opts *Options, validator func(string) bool) *OauthProxy {
	login, _ := url.Parse("https://accounts.google.com/o/oauth2/auth")
	redeem, _ := url.Parse("https://accounts.google.com/o/oauth2/token")
	serveMuxes := map[string]*http.ServeMux{}
	for sub, urls := range opts.proxyUrls {
		serveMux := http.NewServeMux()
		serveMuxes[sub] = serveMux
		for _, u := range urls {
			path := u.Path
			u.Path = ""
			log.Printf("mapping path %q for %q => upstream %q", path, sub, u)
			serveMux.Handle(path, httputil.NewSingleHostReverseProxy(u))
		}
	}
	redirectUrl := opts.redirectUrl
	redirectUrl.Path = oauthCallbackPath

	log.Printf("OauthProxy configured for %s", opts.ClientID)
	domain := opts.CookieDomain
	if domain == "" {
		domain = "<default>"
	}
	log.Printf("Cookie settings: https_only: %v expiry: %s domain:%s", opts.CookieHttpsOnly, opts.CookieExpire, domain)
	return &OauthProxy{
		CookieKey:       "_oauthproxy",
		CookieSeed:      opts.CookieSecret,
		CookieDomain:    opts.CookieDomain,
		CookieHttpsOnly: opts.CookieHttpsOnly,
		CookieExpire:    opts.CookieExpire,
		Validator:       validator,

		clientID:           opts.ClientID,
		clientSecret:       opts.ClientSecret,
		oauthScope:         "profile email",
		oauthRedemptionUrl: redeem,
		oauthLoginUrl:      login,
		serveMuxes:         serveMuxes,
		redirectUrl:        redirectUrl,
		PassBasicAuth:      opts.PassBasicAuth,
	}
}

func (p *OauthProxy) GetLoginURL(redirectUrl string) string {
	params := url.Values{}
	params.Add("redirect_uri", p.redirectUrl.String())
	params.Add("approval_prompt", "force")
	params.Add("scope", p.oauthScope)
	params.Add("client_id", p.clientID)
	params.Add("response_type", "code")
	if redirectUrl != "" {
		params.Add("state", redirectUrl)
	}
	return fmt.Sprintf("%s?%s", p.oauthLoginUrl, params.Encode())
}

func apiRequest(req *http.Request) (*simplejson.Json, error) {
	httpclient := &http.Client{}
	resp, err := httpclient.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		log.Printf("got response code %d - %s", resp.StatusCode, body)
		return nil, errors.New("api request returned non 200 status code")
	}
	data, err := simplejson.NewJson(body)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (p *OauthProxy) redeemCode(code string) (string, string, error) {
	if code == "" {
		return "", "", errors.New("missing code")
	}
	params := url.Values{}
	params.Add("redirect_uri", p.redirectUrl.String())
	params.Add("client_id", p.clientID)
	params.Add("client_secret", p.clientSecret)
	params.Add("code", code)
	params.Add("grant_type", "authorization_code")
	req, err := http.NewRequest("POST", p.oauthRedemptionUrl.String(), bytes.NewBufferString(params.Encode()))
	if err != nil {
		log.Printf("failed building request %s", err.Error())
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	json, err := apiRequest(req)
	if err != nil {
		log.Printf("failed making request %s", err)
		return "", "", err
	}
	access_token, err := json.Get("access_token").String()
	if err != nil {
		return "", "", err
	}

	idToken, err := json.Get("id_token").String()
	if err != nil {
		return "", "", err
	}

	// id_token is a base64 encode ID token payload
	// https://developers.google.com/accounts/docs/OAuth2Login#obtainuserinfo
	jwt := strings.Split(idToken, ".")
	b, err := jwtDecodeSegment(jwt[1])
	if err != nil {
		return "", "", err
	}
	data, err := simplejson.NewJson(b)
	if err != nil {
		return "", "", err
	}
	email, err := data.Get("email").String()
	if err != nil {
		return "", "", err
	}

	return access_token, email, nil
}

func jwtDecodeSegment(seg string) ([]byte, error) {
	if l := len(seg) % 4; l > 0 {
		seg += strings.Repeat("=", 4-l)
	}

	return base64.URLEncoding.DecodeString(seg)
}

func (p *OauthProxy) Domain(req *http.Request) (sub, domain string) {
	domain = strings.Split(req.Host, ":")[0] // strip the port (if any)
	if p.CookieDomain != "" && strings.HasSuffix(domain, p.CookieDomain) {
		sub = domain[:len(domain)-len(p.CookieDomain)]
		domain = p.CookieDomain
	}
	return sub, domain
}

func (p *OauthProxy) ClearCookie(rw http.ResponseWriter, req *http.Request) {
	_, domain := p.Domain(req)
	cookie := &http.Cookie{
		Name:     p.CookieKey,
		Value:    "",
		Path:     "/",
		Domain:   domain,
		Expires:  time.Now().Add(time.Duration(1) * time.Hour * -1),
		HttpOnly: true,
	}
	http.SetCookie(rw, cookie)
}

func (p *OauthProxy) SetCookie(rw http.ResponseWriter, req *http.Request, val string) {
	_, domain := p.Domain(req)
	cookie := &http.Cookie{
		Name:     p.CookieKey,
		Value:    signedCookieValue(p.CookieSeed, p.CookieKey, val),
		Path:     "/",
		Domain:   domain,
		HttpOnly: true,
		Secure:   p.CookieHttpsOnly,
		Expires:  time.Now().Add(p.CookieExpire),
	}
	http.SetCookie(rw, cookie)
}

func (p *OauthProxy) PingPage(rw http.ResponseWriter) {
	rw.WriteHeader(http.StatusOK)
	fmt.Fprintf(rw, "OK")
}

func (p *OauthProxy) ErrorPage(rw http.ResponseWriter, code int, title string, message string) {
	log.Printf("ErrorPage %d %s %s", code, title, message)
	rw.WriteHeader(code)
	templates := getTemplates()
	t := struct {
		Title   string
		Message string
	}{
		Title:   fmt.Sprintf("%d %s", code, title),
		Message: message,
	}
	templates.ExecuteTemplate(rw, "error.html", t)
}

func (p *OauthProxy) SignInPage(rw http.ResponseWriter, req *http.Request, code int) {
	p.ClearCookie(rw, req)
	rw.WriteHeader(code)
	templates := getTemplates()

	t := struct {
		SignInMessage string
		Htpasswd      bool
		Redirect      string
		Version       string
	}{
		SignInMessage: p.SignInMessage,
		Htpasswd:      p.HtpasswdFile != nil,
		Redirect:      req.URL.RequestURI(),
		Version:       VERSION,
	}
	templates.ExecuteTemplate(rw, "sign_in.html", t)
}

func (p *OauthProxy) ManualSignIn(rw http.ResponseWriter, req *http.Request) (string, bool) {
	if req.Method != "POST" || p.HtpasswdFile == nil {
		return "", false
	}
	user := req.FormValue("username")
	passwd := req.FormValue("password")
	if user == "" {
		return "", false
	}
	// check auth
	if p.HtpasswdFile.Validate(user, passwd) {
		log.Printf("authenticated %s via manual sign in", user)
		return user, true
	}
	return "", false
}

func (p *OauthProxy) GetRedirect(req *http.Request) (string, error) {
	err := req.ParseForm()

	if err != nil {
		return "", err
	}

	redirect := req.FormValue("rd")

	if redirect == "" {
		redirect = "/"
	}
	if sub, _ := p.Domain(req); sub != "" {
		u := *req.URL
		u.Scheme = "https"
		u.Host = sub + p.CookieDomain
		u.Path = redirect
		redirect = u.String()
	}

	return redirect, err
}

func (p *OauthProxy) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// check if this is a redirect back at the end of oauth
	remoteAddr := req.RemoteAddr
	if req.Header.Get("X-Real-IP") != "" {
		remoteAddr += fmt.Sprintf(" (%q)", req.Header.Get("X-Real-IP"))
	}
	log.Println(req.Host, req.Header.Get("Host"), remoteAddr, req.Method, req.URL.RequestURI())

	var ok bool
	var user string
	var email string

	if req.URL.Path == pingPath {
		p.PingPage(rw)
		return
	}

	if req.URL.Path == signInPath {
		redirect, err := p.GetRedirect(req)
		if err != nil {
			p.ErrorPage(rw, 500, "Internal Error", err.Error())
			return
		}

		user, ok = p.ManualSignIn(rw, req)
		if ok {
			p.SetCookie(rw, req, user)
			http.Redirect(rw, req, redirect, 302)
		} else {
			p.SignInPage(rw, req, 200)
		}
		return
	}
	if req.URL.Path == oauthStartPath {
		redirect, err := p.GetRedirect(req)
		if err != nil {
			p.ErrorPage(rw, 500, "Internal Error", err.Error())
			return
		}
		http.Redirect(rw, req, p.GetLoginURL(redirect), 302)
		return
	}
	if req.URL.Path == oauthCallbackPath {
		// finish the oauth cycle
		err := req.ParseForm()
		if err != nil {
			p.ErrorPage(rw, 500, "Internal Error", err.Error())
			return
		}
		errorString := req.Form.Get("error")
		if errorString != "" {
			p.ErrorPage(rw, 403, "Permission Denied", errorString)
			return
		}

		_, email, err := p.redeemCode(req.Form.Get("code"))
		if err != nil {
			log.Printf("%s error redeeming code %s", remoteAddr, err)
			p.ErrorPage(rw, 500, "Internal Error", err.Error())
			return
		}

		// set cookie, or deny
		if p.Validator(email) {
			log.Printf("%s authenticating %s completed", remoteAddr, email)
			p.SetCookie(rw, req, email)
			redirect := req.Form.Get("state")
			if redirect == "" {
				redirect = "/"
			}
			http.Redirect(rw, req, redirect, 302)
			return
		} else {
			p.ErrorPage(rw, 403, "Permission Denied", "Invalid Account")
			return
		}
	}

	if !ok {
		cookie, err := req.Cookie(p.CookieKey)
		if err == nil {
			email, ok = validateCookie(cookie, p.CookieSeed)
			user = strings.Split(email, "@")[0]
		}
	}

	if !ok {
		user, ok = p.CheckBasicAuth(req)
		// if we want to promote basic auth requests to cookie'd requests, we could do that here
		// not sure that would be ideal in all circumstances though
		// if ok {
		// 	p.SetCookie(rw, req, user)
		// }
	}

	if !ok {
		log.Printf("%s - invalid cookie session", remoteAddr)
		p.SignInPage(rw, req, 403)
		return
	}

	// At this point, the user is authenticated. proxy normally
	if p.PassBasicAuth {
		req.SetBasicAuth(user, "")
		req.Header["X-Forwarded-User"] = []string{user}
		req.Header["X-Forwarded-Email"] = []string{email}
	}

	sub, _ := p.Domain(req)
	sm := p.serveMuxes[""]
	if m, ok := p.serveMuxes[sub]; ok {
		sm = m
	} else if m, ok = p.serveMuxes["*"]; ok {
		sm = m
	}
	if sm == nil {
		p.ErrorPage(rw, 404, "Page not found", "Invalid Host")
		return
	}
	sm.ServeHTTP(rw, req)
}

func (p *OauthProxy) CheckBasicAuth(req *http.Request) (string, bool) {
	if p.HtpasswdFile == nil {
		return "", false
	}
	s := strings.SplitN(req.Header.Get("Authorization"), " ", 2)
	if len(s) != 2 || s[0] != "Basic" {
		return "", false
	}
	b, err := base64.StdEncoding.DecodeString(s[1])
	if err != nil {
		return "", false
	}
	pair := strings.SplitN(string(b), ":", 2)
	if len(pair) != 2 {
		return "", false
	}
	if p.HtpasswdFile.Validate(pair[0], pair[1]) {
		log.Printf("authenticated %s via basic auth", pair[0])
		return pair[0], true
	}
	return "", false
}

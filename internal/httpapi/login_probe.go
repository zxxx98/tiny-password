package httpapi

import (
	"context"
	"errors"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	loginProbeMaxURLBytes   = 2048
	loginProbeMaxBodyBytes  = 256 << 10
	loginProbeTimeout       = 5 * time.Second
	loginProbeMaxCandidates = 8
)

var (
	loginProbePasswordRE        = regexp.MustCompile(`(?is)<input\b[^>]*\btype\s*=\s*["']?\s*password\b`)
	loginProbeSchemeRE          = regexp.MustCompile(`^[a-z][a-z\d+.-]*:`)
	loginProbeCurrentPasswordRE = regexp.MustCompile(`(?is)<input\b[^>]*\bautocomplete\s*=\s*["']?\s*current-password\b`)
	loginProbeIdentityRE        = regexp.MustCompile(`(?is)<input\b[^>]*(?:type\s*=\s*["']?(?:email|text)|autocomplete\s*=\s*["']?(?:username|email))\b`)
	loginProbeAnchorRE          = regexp.MustCompile(`(?is)<a\b[^>]*\bhref\s*=\s*["']([^"']+)["'][^>]*>(.*?)</a\s*>`)
	loginProbeTagRE             = regexp.MustCompile(`(?is)<[^>]+>`)
	loginProbeLoginWordsRE      = regexp.MustCompile(`(?i)\b(?:log[ -]?in|sign[ -]?in|登录|登入)\b`)
	loginProbeChineseLoginRE    = regexp.MustCompile(`登录|登入`)
	loginProbeActionRE          = regexp.MustCompile(`(?is)<(?:button|input)\b[^>]*(?:type\s*=\s*["']?submit|value\s*=\s*["'][^"']*(?:log[ -]?in|sign[ -]?in|登录|登入))`)
	loginProbeNegativeRE        = regexp.MustCompile(`(?i)\b(?:sign[ -]?up|register|create account|forgot password|reset password|注册|创建账户|忘记密码|重置密码)\b`)
	loginProbeChineseNegativeRE = regexp.MustCompile(`注册|创建账户|忘记密码|重置密码`)
	loginProbeNewPasswordRE     = regexp.MustCompile(`(?is)<input\b[^>]*\bautocomplete\s*=\s*["']?\s*new-password\b`)
)

var errLoginProbeBlocked = errors.New("login probe target is not allowed")

type loginProbeAnalysis struct {
	Score         int
	PasswordField bool
	LoginSignal   bool
	Reasons       []string
}

type loginProbeCandidate struct {
	URL           string   `json:"url"`
	Score         int      `json:"score"`
	PasswordField bool     `json:"password_field"`
	Reasons       []string `json:"reasons"`
}

type loginProbeResponse struct {
	InputURL   string                `json:"input_url"`
	Candidates []loginProbeCandidate `json:"candidates"`
}

type loginProbeResult struct {
	analysis loginProbeAnalysis
	status   int
	body     []byte
}

func normalizeLoginProbeURL(raw string) (*url.URL, error) {
	if len([]byte(raw)) == 0 || len([]byte(raw)) > loginProbeMaxURLBytes {
		return nil, errors.New("URL is empty or too long")
	}
	value := strings.TrimSpace(raw)
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return nil, errors.New("URL is invalid")
	}
	if scheme := loginProbeSchemeRE.FindString(value); scheme != "" && !strings.HasPrefix(strings.ToLower(value), "http://") && !strings.HasPrefix(strings.ToLower(value), "https://") && !strings.HasPrefix(value, "//") {
		return nil, errors.New("URL scheme is not supported")
	}
	if strings.HasPrefix(value, "//") {
		value = "https:" + value
	} else if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil {
		return nil, errors.New("URL is invalid")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("URL scheme is not supported")
	}
	if u.Port() == "" {
		// Accessing u.Port validates malformed bracketed hosts; url.Parse alone
		// otherwise permits several forms that net/http rejects later.
		if strings.Contains(u.Host, ":") && !strings.Contains(u.Host, "]") {
			return nil, errors.New("URL host is invalid")
		}
	} else {
		if _, err := net.LookupPort("tcp", u.Port()); err != nil {
			return nil, errors.New("URL port is invalid")
		}
	}
	if blockedLoginProbeHost(u.Hostname()) {
		return nil, errLoginProbeBlocked
	}
	if u.Path == "" {
		u.Path = "/"
	}
	u.Fragment = ""
	u.RawFragment = ""
	return u, nil
}

func blockedLoginProbeHost(host string) bool {
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") || strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") {
		return true
	}
	if addr, err := netip.ParseAddr(lower); err == nil {
		return blockedLoginProbeIP(addr)
	}
	return false
}

func blockedLoginProbeIP(addr netip.Addr) bool {
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsUnspecified() || addr.IsMulticast() {
		return true
	}
	for _, prefix := range []string{"100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24"} {
		if p, err := netip.ParsePrefix(prefix); err == nil && p.Contains(addr) {
			return true
		}
	}
	return false
}

func analyzeLoginHTML(body []byte) loginProbeAnalysis {
	text := string(body)
	visible := loginProbeTagRE.ReplaceAllString(text, " ")
	visible = html.UnescapeString(visible)
	analysis := loginProbeAnalysis{}
	if loginProbePasswordRE.MatchString(text) {
		analysis.Score += 5
		analysis.PasswordField = true
		analysis.Reasons = append(analysis.Reasons, "检测到密码输入框")
	}
	if loginProbeCurrentPasswordRE.MatchString(text) {
		analysis.Score += 2
		analysis.Reasons = append(analysis.Reasons, "密码框标记为当前密码")
	}
	if loginProbeIdentityRE.MatchString(text) {
		analysis.Score += 2
		analysis.Reasons = append(analysis.Reasons, "检测到用户名或邮箱输入框")
	}
	if loginProbeLoginWordsRE.MatchString(visible) || loginProbeLoginWordsRE.MatchString(text) || loginProbeChineseLoginRE.MatchString(visible) || loginProbeChineseLoginRE.MatchString(text) {
		analysis.Score += 2
		analysis.LoginSignal = true
		analysis.Reasons = append(analysis.Reasons, "检测到登录文案")
	}
	if loginProbeActionRE.MatchString(text) {
		analysis.Score++
		analysis.Reasons = append(analysis.Reasons, "检测到登录提交按钮")
	}
	if loginProbeNegativeRE.MatchString(visible) || loginProbeChineseNegativeRE.MatchString(visible) {
		analysis.Score -= 3
		analysis.Reasons = append(analysis.Reasons, "页面包含注册或密码找回文案")
	}
	if loginProbeNewPasswordRE.MatchString(text) {
		analysis.Score -= 4
		analysis.Reasons = append(analysis.Reasons, "密码框标记为新密码")
	}
	return analysis
}

func safeLoginProbeClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           safeLoginProbeDialContext,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: 4 * time.Second,
		DisableKeepAlives:     true,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   loginProbeTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || blockedLoginProbeHost(req.URL.Hostname()) {
				return errLoginProbeBlocked
			}
			previous := via[len(via)-1].URL
			if !strings.EqualFold(previous.Hostname(), req.URL.Hostname()) || previous.Port() != req.URL.Port() {
				return errLoginProbeBlocked
			}
			if previous.Scheme == "https" && req.URL.Scheme != "https" {
				return errLoginProbeBlocked
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errLoginProbeBlocked
			}
			return nil
		},
	}
}

func safeLoginProbeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errLoginProbeBlocked
	}
	if blockedLoginProbeHost(host) {
		return nil, errLoginProbeBlocked
	}
	var ips []netip.Addr
	if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
		ips = []netip.Addr{addr}
	} else {
		resolved, lookupErr := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if lookupErr != nil || len(resolved) == 0 {
			return nil, errLoginProbeBlocked
		}
		ips = resolved
	}
	for _, ip := range ips {
		if blockedLoginProbeIP(ip) {
			return nil, errLoginProbeBlocked
		}
	}
	dialer := net.Dialer{}
	for _, ip := range ips {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		err = dialErr
	}
	return nil, err
}

func fetchLoginProbe(ctx context.Context, client *http.Client, u *url.URL) (loginProbeResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return loginProbeResult{}, err
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.Header.Set("User-Agent", "TinyPasswordLoginProbe/1.0")
	response, err := client.Do(request)
	if err != nil {
		return loginProbeResult{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, loginProbeMaxBodyBytes+1))
	if err != nil {
		return loginProbeResult{}, err
	}
	if len(body) > loginProbeMaxBodyBytes {
		return loginProbeResult{}, errors.New("response too large")
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if contentType != "" && contentType != "text/html" && contentType != "application/xhtml+xml" {
		return loginProbeResult{status: response.StatusCode}, nil
	}
	return loginProbeResult{analysis: analyzeLoginHTML(body), status: response.StatusCode, body: body}, nil
}

func loginProbeLinks(body []byte, base *url.URL) []*url.URL {
	var links []*url.URL
	seen := map[string]bool{}
	for _, match := range loginProbeAnchorRE.FindAllSubmatch(body, -1) {
		href, err := url.PathUnescape(string(match[1]))
		if err != nil {
			continue
		}
		text := loginProbeTagRE.ReplaceAllString(string(match[2]), " ")
		if !loginProbeLoginWordsRE.MatchString(text) && !loginProbeLoginWordsRE.MatchString(href) {
			continue
		}
		candidate, err := base.Parse(strings.TrimSpace(html.UnescapeString(href)))
		if err != nil || candidate.Hostname() == "" || candidate.Scheme != base.Scheme || !strings.EqualFold(candidate.Hostname(), base.Hostname()) || candidate.Port() != base.Port() || blockedLoginProbeHost(candidate.Hostname()) {
			continue
		}
		candidate.Fragment = ""
		if !seen[candidate.String()] {
			seen[candidate.String()] = true
			links = append(links, candidate)
		}
		if len(links) >= 4 {
			break
		}
	}
	return links
}

func probeLoginPage(ctx context.Context, raw string, client *http.Client) (loginProbeResponse, error) {
	base, err := normalizeLoginProbeURL(raw)
	if err != nil {
		return loginProbeResponse{}, err
	}
	if client == nil {
		client = safeLoginProbeClient()
	}
	ctx, cancel := context.WithTimeout(ctx, loginProbeTimeout)
	defer cancel()
	urls := []*url.URL{base}
	seen := map[string]bool{base.String(): true}
	pathCandidates := []string{"/login", "/signin", "/sign-in", "/auth/login", "/account/login", "/user/login", "/users/sign_in"}
	root := *base
	root.Path, root.RawPath, root.RawQuery, root.Fragment = "", "", "", ""
	if root.String() != base.String() {
		urls = append(urls, &root)
		seen[root.String()] = true
	}
	results := make([]loginProbeCandidate, 0, loginProbeMaxCandidates)
	for i := 0; len(urls) > 0 && i < loginProbeMaxCandidates; i++ {
		u := urls[0]
		urls = urls[1:]
		result, fetchErr := fetchLoginProbe(ctx, client, u)
		if fetchErr != nil {
			continue
		}
		if i == 0 && len(result.body) > 0 {
			for _, link := range loginProbeLinks(result.body, base) {
				if !seen[link.String()] {
					seen[link.String()] = true
					urls = append(urls, link)
				}
			}
		}
		if i < len(pathCandidates) {
			candidate := *base
			candidate.Path, candidate.RawPath, candidate.RawQuery, candidate.Fragment = pathCandidates[i], "", "", ""
			if !seen[candidate.String()] {
				seen[candidate.String()] = true
				urls = append(urls, &candidate)
			}
		}
		if result.status >= 200 && result.status < 400 && (result.analysis.Score >= 3 || (u.String() == base.String() && result.analysis.LoginSignal)) {
			results = append(results, loginProbeCandidate{URL: u.String(), Score: result.analysis.Score, PasswordField: result.analysis.PasswordField, Reasons: result.analysis.Reasons})
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	return loginProbeResponse{InputURL: base.String(), Candidates: results}, nil
}

func (d ItemsDeps) probeLoginPage(w http.ResponseWriter, r *http.Request) {
	var input struct {
		URL string `json:"url"`
	}
	if !decodeJSONBody(w, r, &input, authMaxBodyBytes) {
		return
	}
	result, err := probeLoginPage(r.Context(), input.URL, d.LoginProbeClient)
	if err != nil {
		if errors.Is(err, errLoginProbeBlocked) {
			writeError(w, r, http.StatusBadRequest, "URL_NOT_ALLOWED", "该网址不允许进行登录页探测")
		} else {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "请输入有效的网址")
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}

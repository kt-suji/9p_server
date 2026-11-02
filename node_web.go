package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	webPageChunkRunes = 8000 // 1ページあたりの表示文字数 (more / page N でページ送り可能)
	maxExtractedLinks = 200  // 1ページ内で番号付けする最大リンク数
	maxHistoryDepth   = 30   // ブラウザ履歴 (back / forward) の最大保持数
)

// PageLink はページ内または検索結果内の番号付きリンク `[1]..[N]` を表します。
type PageLink struct {
	Index      int
	Label      string
	URL        string
	DisplayURL string
}

// WebPageState は履歴スタックに保存される1画面分のスナップショットです。
type WebPageState struct {
	Mode          string // "page" または "search"
	Title         string
	Target        string // URL または 検索キーワード
	FullText      string // ページ全文または検索結果全文
	Links         []PageLink
	CurrentChunk  int    // 1始まりのページ送りチャンク番号
	FilterKeyword string // ページ内検索 (/keyword) が適用されている場合のキーワード
	UpdatedAt     string
}

var (
	webMu         sync.Mutex
	webStatus     = "idle" // "idle", "running", "finished", "error"
	webErrorMsg   = ""
	webHistory    []WebPageState
	webHistoryIdx = -1 // 現在表示中の履歴インデックス
)

var (
	reHTMLComments = regexp.MustCompile(`(?s)<!--.*?-->`)
	reStripBlocks  = []*regexp.Regexp{
		regexp.MustCompile(`(?si)<script\b(?:"[^"]*"|'[^']*'|[^'">])*?>.*?</script>`),
		regexp.MustCompile(`(?si)<style\b(?:"[^"]*"|'[^']*'|[^'">])*?>.*?</style>`),
		regexp.MustCompile(`(?si)<svg\b(?:"[^"]*"|'[^']*'|[^'">])*?>.*?</svg>`),
		regexp.MustCompile(`(?si)<noscript\b(?:"[^"]*"|'[^']*'|[^'">])*?>.*?</noscript>`),
		regexp.MustCompile(`(?si)<template\b(?:"[^"]*"|'[^']*'|[^'">])*?>.*?</template>`),
		regexp.MustCompile(`(?si)<iframe\b(?:"[^"]*"|'[^']*'|[^'">])*?>.*?</iframe>`),
		regexp.MustCompile(`(?si)<nav\b(?:"[^"]*"|'[^']*'|[^'">])*?>.*?</nav>`),
		regexp.MustCompile(`(?si)<footer\b(?:"[^"]*"|'[^']*'|[^'">])*?>.*?</footer>`),
		regexp.MustCompile(`(?si)<header\b(?:"[^"]*"|'[^']*'|[^'">])*?>.*?</header>`),
		regexp.MustCompile(`(?si)<aside\b(?:"[^"]*"|'[^']*'|[^'">])*?>.*?</aside>`),
	}
	reAnchorTag  = regexp.MustCompile(`(?si)<a\b((?:"[^"]*"|'[^']*'|[^'">])*)>(.*?)</a>`)
	reHrefAttr   = regexp.MustCompile(`(?i)\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	reBlockTag   = regexp.MustCompile(`(?i)</?(br|p|div|h[1-6]|li|tr|table|article|section|blockquote|pre|ul|ol|dl|dt|dd|hr)\b(?:"[^"]*"|'[^']*'|[^'">])*>`)
	reAnyHTMLTag = regexp.MustCompile(`<(?:"[^"]*"|'[^']*'|[^'">])*>`)
	reSpaces     = regexp.MustCompile(`[ \t\f\r]+`)
	reLinkMarker = regexp.MustCompile(`\[(\d+)\]`)
	reMdLink     = regexp.MustCompile(`\[([^\[\]]+)\]\((https?://[^\s\)]+)\)`)
)

func init() {
	RegisterFeature(FeatureModule{
		Order:   55,
		Name:    "web",
		Summary: "/net/web, /net/search       [RW] 遷移対応Webブラウザ & 検索 (書込例: 'Ryzen', '1', 'back', 'more', '/word')",
		ServerNodes: func(cfg *Config) []NodeDefinition {
			return []NodeDefinition{
				{
					Path: "net/web",
					ReadFn: func() ([]byte, error) {
						return renderUnifiedWebView("net/web"), nil
					},
					WriteFn: func(data []byte) error {
						cmdStr, err := extractUnifiedCommand(data)
						if err != nil {
							webMu.Lock()
							webStatus = "error"
							webErrorMsg = err.Error()
							webMu.Unlock()
							return err
						}
						return handleUnifiedWebCommand(cmdStr, false)
					},
				},
				{
					Path: "net/search",
					ReadFn: func() ([]byte, error) {
						return renderUnifiedWebView("net/search"), nil
					},
					WriteFn: func(data []byte) error {
						cmdStr, err := extractUnifiedCommand(data)
						if err != nil {
							webMu.Lock()
							webStatus = "error"
							webErrorMsg = err.Error()
							webMu.Unlock()
							return err
						}
						return handleUnifiedWebCommand(cmdStr, true)
					},
				},
			}
		},
	})
}

// readableURL はパーセントエンコードされた日本語URLを人間・LLMが読みやすい表記に変換します。
func readableURL(rawURL string) string {
	if unescaped, err := url.QueryUnescape(rawURL); err == nil {
		return unescaped
	}
	return rawURL
}

// shouldIgnoreLink はページ遷移に不要なリンク（画像・スクリプト・編集用リンク等）を除外します。
func shouldIgnoreLink(u *url.URL, base *url.URL) bool {
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return true
	}
	if base != nil && u.Host == base.Host && u.Path == base.Path && u.RawQuery == base.RawQuery {
		return true
	}
	lowerPath := strings.ToLower(u.Path)
	badExts := []string{
		".jpg", ".jpeg", ".png", ".gif", ".svg", ".webp", ".ico", ".bmp",
		".css", ".js", ".woff", ".woff2", ".ttf", ".eot", ".mp4", ".mp3", ".zip", ".gz",
	}
	for _, ext := range badExts {
		if strings.HasSuffix(lowerPath, ext) {
			return true
		}
	}
	lowerQuery := strings.ToLower(u.RawQuery)
	if strings.Contains(lowerQuery, "action=edit") ||
		strings.Contains(lowerQuery, "redlink=1") ||
		strings.Contains(lowerPath, "/special:") ||
		strings.Contains(lowerPath, "/特別:") ||
		strings.Contains(lowerPath, "/ファイル:") ||
		strings.Contains(lowerPath, "/file:") {
		return true
	}
	return false
}

// parseHTMLWithLinks はHTMLから不要タグを除去しつつ、<a href="..."> を [番号] 付きのテキストとリンク表に変換します。
func parseHTMLWithLinks(rawHTML string, baseURL *url.URL) (string, []PageLink) {
	s := reHTMLComments.ReplaceAllString(rawHTML, " ")
	for _, re := range reStripBlocks {
		s = re.ReplaceAllString(s, " ")
	}

	var links []PageLink
	urlToIdx := make(map[string]int)

	s = reAnchorTag.ReplaceAllStringFunc(s, func(m string) string {
		sub := reAnchorTag.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		attrs := sub[1]
		inner := sub[2]

		label := strings.TrimSpace(html.UnescapeString(reAnyHTMLTag.ReplaceAllString(inner, " ")))
		label = reSpaces.ReplaceAllString(label, " ")
		if label == "" {
			return ""
		}

		hm := reHrefAttr.FindStringSubmatch(attrs)
		if len(hm) == 0 {
			return label
		}
		rawHref := hm[1]
		if rawHref == "" {
			rawHref = hm[2]
		}
		if rawHref == "" {
			rawHref = hm[3]
		}
		rawHref = strings.TrimSpace(html.UnescapeString(rawHref))
		if rawHref == "" || strings.HasPrefix(rawHref, "#") || strings.HasPrefix(strings.ToLower(rawHref), "javascript:") {
			return label
		}

		parsedRef, err := url.Parse(rawHref)
		if err != nil {
			return label
		}
		resolved := parsedRef
		if baseURL != nil {
			resolved = baseURL.ResolveReference(parsedRef)
		}
		resolved.Fragment = ""
		if shouldIgnoreLink(resolved, baseURL) {
			return label
		}

		finalURL := resolved.String()
		idx, exists := urlToIdx[finalURL]
		if !exists {
			if len(links) >= maxExtractedLinks {
				return label
			}
			idx = len(links) + 1
			urlToIdx[finalURL] = idx
			links = append(links, PageLink{
				Index:      idx,
				Label:      label,
				URL:        finalURL,
				DisplayURL: readableURL(finalURL),
			})
		}
		return fmt.Sprintf("%s[%d]", label, idx)
	})

	s = reBlockTag.ReplaceAllString(s, "\n")
	s = reAnyHTMLTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)

	lines := strings.Split(s, "\n")
	var out []string
	for _, l := range lines {
		t := strings.TrimSpace(reSpaces.ReplaceAllString(l, " "))
		if t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n"), links
}

// parseMarkdownLinks は Jina Reader フォールバック時の Markdown リンク `[Label](URL)` を `[番号]` に変換します。
func parseMarkdownLinks(md string) (string, []PageLink) {
	var links []PageLink
	urlToIdx := make(map[string]int)

	converted := reMdLink.ReplaceAllStringFunc(md, func(m string) string {
		sub := reMdLink.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		label := strings.TrimSpace(sub[1])
		rawURL := strings.TrimSpace(sub[2])
		if label == "" || rawURL == "" {
			return m
		}
		idx, exists := urlToIdx[rawURL]
		if !exists {
			if len(links) >= maxExtractedLinks {
				return label
			}
			idx = len(links) + 1
			urlToIdx[rawURL] = idx
			links = append(links, PageLink{
				Index:      idx,
				Label:      label,
				URL:        rawURL,
				DisplayURL: readableURL(rawURL),
			})
		}
		return fmt.Sprintf("%s[%d]", label, idx)
	})
	return converted, links
}

// decodeBingURL はBingの検索結果リダイレクトURLから本来のターゲットURLを復号します。
func decodeBingURL(u string) string {
	if !strings.Contains(u, "bing.com/ck/a?") {
		return u
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return u
	}
	rawU := parsed.Query().Get("u")
	if strings.HasPrefix(rawU, "a1") {
		b64 := rawU[2:]
		switch len(b64) % 4 {
		case 2:
			b64 += "=="
		case 3:
			b64 += "="
		}
		if decoded, err := base64.URLEncoding.DecodeString(b64); err == nil {
			return string(decoded)
		}
		if decoded, err := base64.StdEncoding.DecodeString(b64); err == nil {
			return string(decoded)
		}
	}
	return u
}

type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// executeMultiSearch は Yahoo! Japan (Google検索エンジン基盤) -> Bing -> Wikipedia の順でフォールバック検索を行います。
func executeMultiSearch(query string) ([]SearchResult, error) {
	if res, err := executeYahooSearch(query); err == nil && len(res) > 0 {
		return res, nil
	}
	if res, err := executeBingSearch(query); err == nil && len(res) > 0 {
		return res, nil
	}
	return executeWikiSearchFallback(query)
}

func executeYahooSearch(query string) ([]SearchResult, error) {
	searchURL := "https://search.yahoo.co.jp/search?p=" + url.QueryEscape(query)
	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "ja,en-US;q=0.9,en;q=0.8")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}

	htmlContent := decodeConsoleOutput(body)
	reCard := regexp.MustCompile(`(?s)<div class="sw-Card__title">\s*<a href="([^"]+)"[^>]*>.*?<h3[^>]*>(.*?)</h3>.*?</a>(?:.*?<p class="sw-Card__summary[^"]*">(.*?)</p>)?`)
	matches := reCard.FindAllStringSubmatch(htmlContent, 12)

	var results []SearchResult
	for _, m := range matches {
		rawURL := strings.TrimSpace(html.UnescapeString(m[1]))
		title := strings.TrimSpace(html.UnescapeString(reSpaces.ReplaceAllString(reAnyHTMLTag.ReplaceAllString(m[2], " "), " ")))
		snippet := ""
		if len(m) >= 4 {
			snippet = strings.TrimSpace(html.UnescapeString(reSpaces.ReplaceAllString(reAnyHTMLTag.ReplaceAllString(m[3], " "), " ")))
		}
		if title != "" && strings.HasPrefix(rawURL, "http") {
			results = append(results, SearchResult{
				Title:   title,
				URL:     rawURL,
				Snippet: snippet,
			})
		}
	}
	return results, nil
}

func executeBingSearch(query string) ([]SearchResult, error) {
	searchURL := "https://www.bing.com/search?q=" + url.QueryEscape(query)
	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "ja,en-US;q=0.9,en;q=0.8")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}

	htmlContent := decodeConsoleOutput(body)
	reAlgo := regexp.MustCompile(`(?s)<li class="b_algo"[^>]*>(.*?)</li>`)
	matches := reAlgo.FindAllStringSubmatch(htmlContent, 12)

	reTitleLink := regexp.MustCompile(`(?s)<h2><a[^>]+href="([^"]+)"[^>]*>(.*?)</a></h2>`)
	reSnippet := regexp.MustCompile(`(?s)<p[^>]*class="b_lineclamp[^"]*"[^>]*>(.*?)</p>`)
	reSnippetFallback := regexp.MustCompile(`(?s)<p[^>]*>(.*?)</p>`)

	var results []SearchResult
	for _, m := range matches {
		block := m[1]
		tlm := reTitleLink.FindStringSubmatch(block)
		if len(tlm) < 3 {
			continue
		}
		finalURL := decodeBingURL(html.UnescapeString(tlm[1]))
		title := html.UnescapeString(reAnyHTMLTag.ReplaceAllString(tlm[2], ""))

		snippet := ""
		if sm := reSnippet.FindStringSubmatch(block); len(sm) >= 2 {
			snippet = html.UnescapeString(reAnyHTMLTag.ReplaceAllString(sm[1], ""))
		} else if sm2 := reSnippetFallback.FindStringSubmatch(block); len(sm2) >= 2 {
			snippet = html.UnescapeString(reAnyHTMLTag.ReplaceAllString(sm2[1], ""))
		}

		if title != "" && finalURL != "" {
			results = append(results, SearchResult{
				Title:   strings.TrimSpace(title),
				URL:     strings.TrimSpace(finalURL),
				Snippet: strings.TrimSpace(snippet),
			})
		}
	}
	return results, nil
}

func executeWikiSearchFallback(query string) ([]SearchResult, error) {
	wikiURL := "https://ja.wikipedia.org/w/api.php?action=query&list=search&srsearch=" + url.QueryEscape(query) + "&format=json"
	req, err := http.NewRequest("GET", wikiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "9P-Web-Agent/1.0")

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var data struct {
		Query struct {
			Search []struct {
				Title   string `json:"title"`
				Snippet string `json:"snippet"`
			} `json:"search"`
		} `json:"query"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	var results []SearchResult
	for _, item := range data.Query.Search {
		snip := html.UnescapeString(reAnyHTMLTag.ReplaceAllString(item.Snippet, ""))
		pageURL := "https://ja.wikipedia.org/wiki/" + url.PathEscape(item.Title)
		results = append(results, SearchResult{
			Title:   item.Title + " (Wikipedia)",
			URL:     pageURL,
			Snippet: snip,
		})
	}
	return results, nil
}

// fetchWebPageWithLinks は指定URLを取得し、タイトル・番号付き本文・リンク配列を返します。
// JS必須サイト(SPA)や403ブロック時は自動的に Jina Reader 経由の取得へフォールバックします。
func fetchWebPageWithLinks(targetURL string) (string, string, string, []PageLink, error) {
	if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
		targetURL = "https://" + targetURL
	}

	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		return "", "", "", nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ja,en-US;q=0.9,en;q=0.8")

	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)

	var finalURL string
	var title string
	var cleaned string
	var links []PageLink

	if err == nil {
		defer resp.Body.Close()
		finalURL = resp.Request.URL.String()
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
			if readErr == nil {
				htmlStr := decodeConsoleOutput(body)
				reTitle := regexp.MustCompile(`(?si)<title\b[^>]*>(.*?)</title>`)
				if tm := reTitle.FindStringSubmatch(htmlStr); len(tm) >= 2 {
					title = strings.TrimSpace(html.UnescapeString(reSpaces.ReplaceAllString(tm[1], " ")))
				}
				cleaned, links = parseHTMLWithLinks(htmlStr, resp.Request.URL)
			}
		}
	}

	// SPAやブロック等で本文が極端に短い場合は Jina Reader にフォールバック
	if len([]rune(cleaned)) < 100 {
		jinaTitle, jinaText, jinaLinks, jinaErr := fetchViaJinaFallback(targetURL)
		if jinaErr == nil && len([]rune(jinaText)) >= len([]rune(cleaned)) {
			if jinaTitle != "" {
				title = jinaTitle
			}
			if finalURL == "" {
				finalURL = targetURL
			}
			return finalURL, title, jinaText, jinaLinks, nil
		}
		if err != nil {
			return "", "", "", nil, err
		}
		if resp != nil && (resp.StatusCode < 200 || resp.StatusCode >= 400) {
			return "", "", "", nil, fmt.Errorf("HTTP %s", resp.Status)
		}
	}

	if finalURL == "" {
		finalURL = targetURL
	}
	return finalURL, title, cleaned, links, nil
}

func fetchViaJinaFallback(targetURL string) (string, string, []PageLink, error) {
	jinaURL := "https://r.jina.ai/" + targetURL
	req, err := http.NewRequest("GET", jinaURL, nil)
	if err != nil {
		return "", "", nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", "", nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", "", nil, err
	}
	raw := decodeConsoleOutput(body)
	title := ""
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "Title:") {
			title = strings.TrimSpace(strings.TrimPrefix(line, "Title:"))
			break
		}
	}
	text, links := parseMarkdownLinks(raw)
	return title, text, links, nil
}

// pushHistoryLocked は webMu ロック保持中に新しいページ状態を履歴スタックへ追加します。
func pushHistoryLocked(st WebPageState) {
	if webHistoryIdx >= 0 && webHistoryIdx < len(webHistory)-1 {
		webHistory = webHistory[:webHistoryIdx+1]
	}
	webHistory = append(webHistory, st)
	if len(webHistory) > maxHistoryDepth {
		webHistory = webHistory[len(webHistory)-maxHistoryDepth:]
	}
	webHistoryIdx = len(webHistory) - 1
}

// parseLinkNumberCommand は "1", "[1]", "#1", "open 1", "goto 1", "link 1", "go 1" からリンク番号を抽出します。
func parseLinkNumberCommand(input string) (int, bool) {
	s := strings.TrimSpace(input)
	lower := strings.ToLower(s)
	for _, prefix := range []string{"open ", "goto ", "link ", "go ", "jump "} {
		if strings.HasPrefix(lower, prefix) {
			s = strings.TrimSpace(s[len(prefix):])
			break
		}
	}
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	s = strings.TrimPrefix(s, "#")
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n, true
	}
	return 0, false
}

// parsePageScrollCommand は "page 2", "p2", "p 2" からチャンク番号を抽出します。
func parsePageScrollCommand(input string) (int, bool) {
	lower := strings.ToLower(strings.TrimSpace(input))
	for _, prefix := range []string{"page ", "p "} {
		if strings.HasPrefix(lower, prefix) {
			if n, err := strconv.Atoi(strings.TrimSpace(lower[len(prefix):])); err == nil && n >= 1 {
				return n, true
			}
		}
	}
	if strings.HasPrefix(lower, "p") && len(lower) > 1 {
		if n, err := strconv.Atoi(lower[1:]); err == nil && n >= 1 {
			return n, true
		}
	}
	return 0, false
}

// handleUnifiedWebCommand は /net/web と /net/search 共通のコマンド処理ディスパッチャです。
func handleUnifiedWebCommand(rawInput string, defaultSearch bool) error {
	input := strings.TrimSpace(rawInput)
	if input == "" {
		return fmt.Errorf("コマンド、URL、リンク番号、または検索キーワードを入力してください")
	}
	lower := strings.ToLower(input)

	webMu.Lock()
	if webStatus == "running" {
		webMu.Unlock()
		return fmt.Errorf("現在Webアクセス処理を実行中です")
	}

	// 1. 履歴「戻る」(back / b / 戻る)
	if lower == "back" || lower == "b" || input == "戻る" {
		if webHistoryIdx > 0 {
			webHistoryIdx--
			webStatus = "finished"
			webErrorMsg = ""
			target := webHistory[webHistoryIdx].Target
			webMu.Unlock()
			addSystemLog(fmt.Sprintf("Web履歴: 前のページへ戻る (%s)", target))
			return nil
		}
		webStatus = "error"
		webErrorMsg = "これ以上前のページ履歴はありません。"
		webMu.Unlock()
		return nil
	}

	// 2. 履歴「進む」(forward / f / 進む)
	if lower == "forward" || lower == "f" || input == "進む" {
		if webHistoryIdx >= 0 && webHistoryIdx < len(webHistory)-1 {
			webHistoryIdx++
			webStatus = "finished"
			webErrorMsg = ""
			target := webHistory[webHistoryIdx].Target
			webMu.Unlock()
			addSystemLog(fmt.Sprintf("Web履歴: 次のページへ進む (%s)", target))
			return nil
		}
		webStatus = "error"
		webErrorMsg = "これ以上先のページ履歴はありません。"
		webMu.Unlock()
		return nil
	}

	// 2b. 履歴一覧の表示・ジャンプ (history / hist / 履歴)
	if lower == "history" || lower == "hist" || input == "履歴" {
		if len(webHistory) == 0 {
			webStatus = "error"
			webErrorMsg = "まだ閲覧履歴はありません。"
			webMu.Unlock()
			return nil
		}
		var sb strings.Builder
		sb.WriteString("📜 閲覧履歴一覧 (元の画面に戻るには 'top'、前後に移動するには 'back' / 'forward'):\n\n")
		for i, h := range webHistory {
			mark := "   "
			if i == webHistoryIdx {
				mark = " 👉"
			}
			sb.WriteString(fmt.Sprintf("%s [H%d] %s (%s) — %s\n", mark, i+1, h.Title, readableURL(h.Target), h.UpdatedAt))
		}
		webHistory[webHistoryIdx].FilterKeyword = "__HISTORY__\n" + sb.String()
		webStatus = "finished"
		webErrorMsg = ""
		webMu.Unlock()
		return nil
	}

	// 2c. 再読み込み (reload / refresh)
	if lower == "reload" || lower == "refresh" || input == "再読込" {
		if webHistoryIdx >= 0 {
			cur := webHistory[webHistoryIdx]
			webStatus = "running"
			webErrorMsg = ""
			webMu.Unlock()
			if cur.Mode == "search" {
				go asyncExecuteSearch(cur.Target)
			} else {
				go asyncFetchPage(cur.Target)
			}
			return nil
		}
		webMu.Unlock()
		return nil
	}

	// 3. ページ送り: 次へ (more / next / down / n)
	if lower == "more" || lower == "next" || lower == "down" || lower == "n" {
		if webHistoryIdx >= 0 {
			cur := &webHistory[webHistoryIdx]
			cur.FilterKeyword = ""
			totalChunks := calcTotalChunks(cur.FullText)
			if cur.CurrentChunk < totalChunks {
				cur.CurrentChunk++
			}
			webStatus = "finished"
			webErrorMsg = ""
		}
		webMu.Unlock()
		return nil
	}

	// 4. ページ送り: 前へ (prev / up / p) / 先頭へ (top)
	if lower == "prev" || lower == "up" || lower == "p" || lower == "top" {
		if webHistoryIdx >= 0 {
			cur := &webHistory[webHistoryIdx]
			cur.FilterKeyword = ""
			if lower == "top" || cur.CurrentChunk <= 1 {
				cur.CurrentChunk = 1
			} else {
				cur.CurrentChunk--
			}
			webStatus = "finished"
			webErrorMsg = ""
		}
		webMu.Unlock()
		return nil
	}

	// 5. 指定ページ番号へジャンプ (page 2 / p2)
	if chunkNum, ok := parsePageScrollCommand(input); ok {
		if webHistoryIdx >= 0 {
			cur := &webHistory[webHistoryIdx]
			cur.FilterKeyword = ""
			totalChunks := calcTotalChunks(cur.FullText)
			if chunkNum > totalChunks {
				chunkNum = totalChunks
			}
			cur.CurrentChunk = chunkNum
			webStatus = "finished"
			webErrorMsg = ""
		}
		webMu.Unlock()
		return nil
	}

	// 6. ページ内キーワード抽出 (/keyword または inpage keyword)
	if strings.HasPrefix(input, "/") && len(input) > 1 && !strings.HasPrefix(lower, "/net/") && !strings.HasPrefix(lower, "/client/") && !strings.HasPrefix(lower, "/server/") {
		kw := strings.TrimSpace(input[1:])
		if webHistoryIdx >= 0 && kw != "" {
			webHistory[webHistoryIdx].FilterKeyword = kw
			webStatus = "finished"
			webErrorMsg = ""
		}
		webMu.Unlock()
		return nil
	}
	if strings.HasPrefix(lower, "inpage ") {
		kw := strings.TrimSpace(input[7:])
		if webHistoryIdx >= 0 && kw != "" {
			webHistory[webHistoryIdx].FilterKeyword = kw
			webStatus = "finished"
			webErrorMsg = ""
		}
		webMu.Unlock()
		return nil
	}

	// 7. リンク番号指定でページ遷移 ("1", "open 1", "[1]" 等)
	if linkNum, ok := parseLinkNumberCommand(input); ok {
		if webHistoryIdx < 0 || len(webHistory[webHistoryIdx].Links) == 0 {
			webStatus = "error"
			webErrorMsg = fmt.Sprintf("リンク [%d] を開けません（現在開いているページまたは検索結果がありません）。先に検索またはURL取得を行ってください。", linkNum)
			webMu.Unlock()
			return nil
		}
		curLinks := webHistory[webHistoryIdx].Links
		if linkNum < 1 || linkNum > len(curLinks) {
			webStatus = "error"
			webErrorMsg = fmt.Sprintf("リンク番号 [%d] は範囲外です（有効範囲: 1 〜 %d）。", linkNum, len(curLinks))
			webMu.Unlock()
			return nil
		}
		chosen := curLinks[linkNum-1]
		webStatus = "running"
		webErrorMsg = ""
		webMu.Unlock()

		addSystemLog(fmt.Sprintf("Webリンク遷移 [%d]: %s (%s)", linkNum, chosen.Label, chosen.URL))
		go asyncFetchPage(chosen.URL)
		return nil
	}

	// 8. URL直接指定かWeb検索かの判定
	isSearch := defaultSearch
	searchQuery := input

	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		isSearch = false
	} else if strings.HasPrefix(lower, "open ") || strings.HasPrefix(lower, "fetch ") || strings.HasPrefix(lower, "get ") {
		parts := strings.Fields(input)
		if len(parts) >= 2 {
			input = parts[1]
			isSearch = false
		}
	} else if strings.HasPrefix(lower, "search ") {
		isSearch = true
		searchQuery = strings.TrimSpace(input[7:])
	} else if strings.HasPrefix(lower, "find ") {
		isSearch = true
		searchQuery = strings.TrimSpace(input[5:])
	} else if strings.HasPrefix(input, "?") {
		isSearch = true
		searchQuery = strings.TrimSpace(input[1:])
	} else if !defaultSearch {
		// /net/web に書き込まれた場合: スペースを含まずドットを含む文字列(例: github.com)ならURL、それ以外は検索
		if !strings.ContainsAny(input, " 　\t") && strings.Contains(input, ".") {
			isSearch = false
		} else {
			isSearch = true
		}
	}

	webStatus = "running"
	webErrorMsg = ""
	webMu.Unlock()

	if isSearch {
		addSystemLog(fmt.Sprintf("Web検索開始: %s", searchQuery))
		go asyncExecuteSearch(searchQuery)
	} else {
		addSystemLog(fmt.Sprintf("Webページ取得開始: %s", input))
		go asyncFetchPage(input)
	}
	return nil
}

func asyncExecuteSearch(query string) {
	start := time.Now()
	results, err := executeMultiSearch(query)
	elapsed := time.Since(start).Seconds()

	webMu.Lock()
	defer webMu.Unlock()

	if err != nil {
		webStatus = "error"
		webErrorMsg = fmt.Sprintf("検索に失敗しました (%q): %v", query, err)
		return
	}

	var sb strings.Builder
	var links []PageLink
	sb.WriteString(fmt.Sprintf("検索キーワード: %q (ヒット件数: %d件)\n", query, len(results)))
	sb.WriteString("💡 記事の本文を読むには、リンク番号（例: '1' または 'open 1'）を書き込んでください。\n\n")

	for i, r := range results {
		idx := i + 1
		dispURL := readableURL(r.URL)
		sb.WriteString(fmt.Sprintf("[%d] %s\n    URL: %s\n    要約: %s\n\n", idx, r.Title, dispURL, r.Snippet))
		links = append(links, PageLink{
			Index:      idx,
			Label:      r.Title,
			URL:        r.URL,
			DisplayURL: dispURL,
		})
	}

	st := WebPageState{
		Mode:         "search",
		Title:        fmt.Sprintf("Web検索: %s (%d件)", query, len(results)),
		Target:       query,
		FullText:     sb.String(),
		Links:        links,
		CurrentChunk: 1,
		UpdatedAt:    fmt.Sprintf("%s (%.2fs)", time.Now().Format("2006-01-02 15:04:05"), elapsed),
	}
	pushHistoryLocked(st)
	webStatus = "finished"
	addSystemLog(fmt.Sprintf("Web検索完了: %q (%d件, %.2fs)", query, len(results), elapsed))
}

func asyncFetchPage(targetURL string) {
	start := time.Now()
	finalURL, title, text, links, err := fetchWebPageWithLinks(targetURL)
	elapsed := time.Since(start).Seconds()

	webMu.Lock()
	defer webMu.Unlock()

	if err != nil {
		webStatus = "error"
		webErrorMsg = fmt.Sprintf("ページ取得に失敗しました (%s): %v", targetURL, err)
		return
	}

	if title == "" {
		title = readableURL(finalURL)
	}

	st := WebPageState{
		Mode:         "page",
		Title:        title,
		Target:       finalURL,
		FullText:     text,
		Links:        links,
		CurrentChunk: 1,
		UpdatedAt:    fmt.Sprintf("%s (%.2fs)", time.Now().Format("2006-01-02 15:04:05"), elapsed),
	}
	pushHistoryLocked(st)
	webStatus = "finished"
	addSystemLog(fmt.Sprintf("Webページ取得完了: %s (%d文字, リンク%d件, %.2fs)", finalURL, len([]rune(text)), len(links), elapsed))
}

func calcTotalChunks(text string) int {
	n := len([]rune(text))
	if n == 0 {
		return 1
	}
	return (n + webPageChunkRunes - 1) / webPageChunkRunes
}

// extractVisibleLinks は現在表示中のテキストスライス内に登場する `[番号]` のリンクを優先抽出します。
func extractVisibleLinks(visibleText string, allLinks []PageLink) []PageLink {
	if len(allLinks) == 0 {
		return nil
	}
	matches := reLinkMarker.FindAllStringSubmatch(visibleText, -1)
	seen := make(map[int]bool)
	var out []PageLink
	for _, m := range matches {
		if idx, err := strconv.Atoi(m[1]); err == nil && idx >= 1 && idx <= len(allLinks) {
			if !seen[idx] {
				seen[idx] = true
				out = append(out, allLinks[idx-1])
			}
		}
	}
	// もし表示チャンク内にリンクマーカーが少なければ先頭から補完
	if len(out) < 10 {
		for _, l := range allLinks {
			if !seen[l.Index] {
				seen[l.Index] = true
				out = append(out, l)
				if len(out) >= 25 {
					break
				}
			}
		}
	}
	if len(out) > 45 {
		out = out[:45]
	}
	return out
}

// filterTextByKeyword はページ全文から指定キーワードを含む行とその前後を抽出します。
func filterTextByKeyword(fullText, kw string) string {
	lines := strings.Split(fullText, "\n")
	lowerKw := strings.ToLower(kw)
	var matched []string
	for i, line := range lines {
		if strings.Contains(strings.ToLower(line), lowerKw) {
			start := i - 1
			if start < 0 {
				start = 0
			}
			end := i + 1
			if end >= len(lines) {
				end = len(lines) - 1
			}
			snippet := strings.Join(lines[start:end+1], " / ")
			matched = append(matched, fmt.Sprintf("  ・L%d: %s", i+1, snippet))
			if len(matched) >= 40 {
				break
			}
		}
	}
	if len(matched) == 0 {
		return fmt.Sprintf("ページ内にキーワード %q は見つかりませんでした。('top' で全文表示に戻ります)", kw)
	}
	return fmt.Sprintf("🔍 ページ内検索 %q の一致箇所 (%d件 — 'top' で全文表示に戻ります):\n\n%s", kw, len(matched), strings.Join(matched, "\n"))
}

func renderUnifiedWebView(filePath string) []byte {
	webMu.Lock()
	status := webStatus
	errMsg := webErrorMsg
	histLen := len(webHistory)
	histIdx := webHistoryIdx
	var cur WebPageState
	if histIdx >= 0 && histIdx < histLen {
		cur = webHistory[histIdx]
	}
	webMu.Unlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== 9P 遷移対応 Webブラウザ & 検索エンジン (%s) ===\n", filePath))
	switch status {
	case "running":
		sb.WriteString("状態 (Status):     RUNNING (データ取得・解析中 ⏳)\n")
	case "error":
		sb.WriteString("状態 (Status):     ERROR (エラー ⚠️)\n")
	case "finished":
		sb.WriteString("状態 (Status):     FINISHED (完了 ✓)\n")
	default:
		sb.WriteString("状態 (Status):     IDLE (待機中)\n")
	}

	if errMsg != "" {
		sb.WriteString(fmt.Sprintf("エラー詳細:        %s\n", errMsg))
	}

	if histIdx >= 0 {
		sb.WriteString(fmt.Sprintf("現在地 (Target):   %s\n", readableURL(cur.Target)))
		sb.WriteString(fmt.Sprintf("タイトル (Title):  %s\n", cur.Title))
		canBack := histIdx > 0
		canFwd := histIdx < histLen-1
		sb.WriteString(fmt.Sprintf("履歴 (History):    %d / %d 階層 (戻る 'back': %v / 進む 'forward': %v) | 更新: %s\n",
			histIdx+1, histLen, canBack, canFwd, cur.UpdatedAt))

		runes := []rune(cur.FullText)
		totalRunes := len(runes)
		totalChunks := calcTotalChunks(cur.FullText)
		chunk := cur.CurrentChunk
		if chunk < 1 {
			chunk = 1
		}
		if chunk > totalChunks {
			chunk = totalChunks
		}

		startIdx := (chunk - 1) * webPageChunkRunes
		endIdx := startIdx + webPageChunkRunes
		if endIdx > totalRunes {
			endIdx = totalRunes
		}

		sb.WriteString(fmt.Sprintf("ページ位置:        ページ %d / %d (%d〜%d文字 / 全%d文字) | リンク総数: %d個\n\n",
			chunk, totalChunks, startIdx+1, endIdx, totalRunes, len(cur.Links)))

		sb.WriteString("--- [クイック入力 (Quick Input)] ---\n")
		sb.WriteString("↓ ここ（'> ' の右側）に 検索語 / リンク番号 / back / more / URL を入力して Ctrl+S (最下部でも入力可) ↓\n")
		sb.WriteString("> \n\n")

		sb.WriteString("--- [本文 / 検索結果 (Content)] ---\n")
		var visibleText string
		if strings.HasPrefix(cur.FilterKeyword, "__HISTORY__\n") {
			visibleText = strings.TrimPrefix(cur.FilterKeyword, "__HISTORY__\n")
		} else if cur.FilterKeyword != "" {
			visibleText = filterTextByKeyword(cur.FullText, cur.FilterKeyword)
		} else if totalRunes == 0 {
			visibleText = "(コンテンツなし)"
		} else {
			visibleText = string(runes[startIdx:endIdx])
			if chunk < totalChunks {
				visibleText += fmt.Sprintf("\n\n... [ページ %d/%d 終了 — 続きを見るには 'more' または 'page %d' を書き込んでください]", chunk, totalChunks, chunk+1)
			}
		}
		sb.WriteString(strings.TrimRight(visibleText, "\r\n") + "\n")

		// ページ表示モードのときは、本文中に登場したリンク一覧を下部にカタログ表示
		if cur.Mode == "page" && len(cur.Links) > 0 {
			visLinks := extractVisibleLinks(visibleText, cur.Links)
			sb.WriteString(fmt.Sprintf("\n--- [遷移可能リンク一覧 (全%d件中 %d件表示 — 番号を書き込むと遷移します)] ---\n", len(cur.Links), len(visLinks)))
			for _, l := range visLinks {
				sb.WriteString(fmt.Sprintf("  [%d] %s  ->  %s\n", l.Index, l.Label, l.DisplayURL))
			}
		}
	} else {
		sb.WriteString("現在地 (Target):   (未実行)\n\n")
		sb.WriteString("--- [クイック入力 (Quick Input)] ---\n")
		sb.WriteString("↓ ここ（'> ' の右側）に 検索語 または URL を入力して Ctrl+S (最下部でも入力可) ↓\n")
		sb.WriteString("> \n\n")
		sb.WriteString("--- [本文 / 検索結果 (Content)] ---\n")
		sb.WriteString("(まだWeb検索またはページ取得は実行されていません)\n")
	}

	sb.WriteString("\n--- [操作コマンド一覧 (人間 & AI 共通)] ---\n")
	sb.WriteString("  ・Web検索する        : キーワードを書き込む        (例: Ryzen 9950X  または  search Go 1.27)\n")
	sb.WriteString("  ・リンク先へ遷移する : リンク番号を書き込む        (例: 1  または  open 3  ※[番号]のページへ進みます)\n")
	sb.WriteString("  ・前のページへ戻る   : back を書き込む             (例: back  /  進む場合は forward)\n")
	sb.WriteString("  ・長文の続きを見る   : more または page N          (例: more  /  page 2  /  top)\n")
	sb.WriteString("  ・ページ内を検索する : /キーワード                 (例: /キャッシュ  /  /ソケット)\n")
	sb.WriteString("  ・URLを直接開く      : URLを書き込む               (例: https://ja.wikipedia.org/wiki/Ryzen)\n\n")

	sb.WriteString("--- [入力欄 (Input)] ---\n")
	sb.WriteString("↓ ここ（'> ' の右側）に 検索語 / リンク番号(1, 2...) / back / more / URL を入力して Ctrl+S ↓\n")
	sb.WriteString("> \n")
	return []byte(sb.String())
}

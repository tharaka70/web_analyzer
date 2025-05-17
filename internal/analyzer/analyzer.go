package analyzer

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync" // For WaitGroup concurrent link checks
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom" // For tag atom comparison
)

// AnalysisResult holds all the extracted information
type AnalysisResult struct {
	HTMLVersion        string
	PageTitle          string
	HeadingsCount      map[string]int // Map with header value and count {"h1": 2, "h2": 5}
	InternalLinksCount int
	ExternalLinksCount int
	InaccessibleLinks  []InaccessibleLinkInfo
	ContainsLoginForm  bool
}

type InaccessibleLinkInfo struct {
	URL        string
	StatusCode int // 0 if DNS error or other non-HTTP error
	Error      string
}

// Custom error type to include status code
type AnalysisError struct {
	Message    string
	StatusCode int
}

func (e *AnalysisError) Error() string {
	return e.Message
}

// FetchAndAnalyze performs the core analysis
func FetchAndAnalyze(pageURL string) (*AnalysisResult, error) {
	slog.Info("Attempting to fetch URL", "url", pageURL)
	resp, err := http.Get(pageURL)
	if err != nil {
		if urlErr, ok := err.(*url.Error); ok {
			slog.Error("Network error fetching URL", "url", pageURL, "error", urlErr)
			return nil, &AnalysisError{Message: fmt.Sprintf("Failed to fetch URL: %v", urlErr), StatusCode: 0}
		}
		slog.Error("Unknown error fetching URL", "url", pageURL, "error", err)
		return nil, &AnalysisError{Message: fmt.Sprintf("Failed to fetch URL: %v", err), StatusCode: 0}
	}
	defer resp.Body.Close()

	slog.Info("Successfully fetched URL", "url", pageURL, "status", resp.Status)

	if resp.StatusCode >= 400 { // Handle HTTP error statuses explicitly
		slog.Warn("URL returned HTTP error status", "url", pageURL, "status_code", resp.StatusCode, "status_text", resp.Status)
		return nil, &AnalysisError{
			Message:    fmt.Sprintf("URL returned HTTP error: %s", resp.Status),
			StatusCode: resp.StatusCode,
		}
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(strings.ToLower(contentType), "text/html") {
		slog.Warn("URL content type is not HTML", "url", pageURL, "content_type", contentType)
		return nil, &AnalysisError{
			Message:    fmt.Sprintf("URL is not an HTML page. Content-Type: %s", contentType),
			StatusCode: resp.StatusCode,
		}
	}

	doc, err := html.Parse(resp.Body)
	if err != nil {
		slog.Error("Failed to parse HTML", "url", pageURL, "error", err)
		return nil, &AnalysisError{Message: fmt.Sprintf("Failed to parse HTML: %v", err), StatusCode: resp.StatusCode}
	}

	result := &AnalysisResult{
		HeadingsCount: make(map[string]int),
	}

	var baseDomain *url.URL
	baseDomain, err = url.Parse(pageURL)
	if err != nil {
		slog.Error("Failed to parse baseDomain from pageURL", "pageURL", pageURL, "error", err)
		return nil, &AnalysisError{Message: fmt.Sprintf("Failed to parse base URL for link analysis: %v", err), StatusCode: resp.StatusCode}
	}

	var linksToTest []string

	// Traverse the HTML tree
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode {
			// --- 1. Page Title ---
			if n.DataAtom == atom.Title && n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
				result.PageTitle = strings.TrimSpace(n.FirstChild.Data)
			}

			// --- 2. Headings Count ---
			switch n.DataAtom {
			case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
				result.HeadingsCount[n.Data]++
				slog.Debug("Found heading", "tag", n.Data, "current_count", result.HeadingsCount[n.Data])
			}

			// --- 3. Links Count (Anchor tags and Link tags) ---
			if n.DataAtom == atom.A || n.DataAtom == atom.Link {
				var hrefAttr string
				// isLinkTagWithStylesheetRel := false

				for _, attr := range n.Attr {
					if attr.Key == "href" {
						hrefAttr = strings.TrimSpace(attr.Val)
					}
					// if n.DataAtom == atom.Link && attr.Key == "rel" && strings.ToLower(attr.Val) == "stylesheet" {
					// 	isLinkTagWithStylesheetRel = true
					// }
				}

				// Only process <link> tags if they are stylesheets (or other link types you want to count)
				// For now, we count all <link href="..."> as links if they have an href.
				// You might want to be more specific, e.g. only `rel="stylesheet"`, `rel="icon"`, etc.
				// For this assignment, counting all <link href> and <a href> seems reasonable.
				if hrefAttr != "" {
					if hrefAttr == "" || strings.HasPrefix(hrefAttr, "#") ||
						strings.HasPrefix(strings.ToLower(hrefAttr), "javascript:") ||
						strings.HasPrefix(strings.ToLower(hrefAttr), "mailto:") ||
						strings.HasPrefix(strings.ToLower(hrefAttr), "tel:") {
						// Skip empty, fragment, javascript, mailto, or tel links
					} else {
						absoluteLink, parseErr := baseDomain.Parse(hrefAttr)
						if parseErr != nil {
							slog.Warn("Could not parse link", "original_href", hrefAttr, "base_url", baseDomain.String(), "error", parseErr)
						} else {
							linkStr := absoluteLink.String()
							if absoluteLink.Host == baseDomain.Host && absoluteLink.Scheme == baseDomain.Scheme { // ensure scheme also matches for stricter internal
								result.InternalLinksCount++
								slog.Debug("Found internal link", "tag", n.Data, "href", linkStr)
							} else {
								result.ExternalLinksCount++
								slog.Debug("Found external link", "tag", n.Data, "href", linkStr)
							}
							linksToTest = append(linksToTest, linkStr)
						}
					}
				}
			}

			// --- 4. Login Form Detection (Basic Heuristics) ---
			if n.DataAtom == atom.Form {
				// Check if ContainsLoginForm is already true to avoid redundant checks if multiple forms exist
				if !result.ContainsLoginForm {
					result.ContainsLoginForm = detectLoginForm(n)
					if result.ContainsLoginForm {
						slog.Info("Login form detected on page")
					}
				}
			}
		} else if n.Type == html.DoctypeNode {
			// --- 5. HTML Version (Check based on Doctype) ---
			slog.Debug("Doctype node found", "data", n.Data)
			publicID := ""
			systemID := ""
			for _, attr := range n.Attr {
				if attr.Key == "public" {
					publicID = strings.TrimSpace(attr.Val)
				} else if attr.Key == "system" {
					systemID = strings.TrimSpace(attr.Val)
				}
			}
			slog.Debug("Doctype IDs", "public", publicID, "system", systemID)

			// Normalize n.Data for comparison (html.Parse makes it lowercase for <!DOCTYPE html>)
			doctypeName := strings.ToLower(n.Data)

			if doctypeName == "html" { // Common for HTML5, HTML 4.01, XHTML
				if publicID == "" && systemID == "" {
					result.HTMLVersion = "HTML5"
				} else if strings.Contains(publicID, "XHTML 1.0 Strict") {
					result.HTMLVersion = "XHTML 1.0 Strict"
				} else if strings.Contains(publicID, "XHTML 1.0 Transitional") {
					result.HTMLVersion = "XHTML 1.0 Transitional"
				} else if strings.Contains(publicID, "HTML 4.01//EN") && strings.Contains(publicID, "Strict") {
					result.HTMLVersion = "HTML 4.01 Strict"
				} else if strings.Contains(publicID, "HTML 4.01 Transitional//EN") { // Often associated with loose.dtd
					result.HTMLVersion = "HTML 4.01 Transitional"
				} else if strings.Contains(publicID, "HTML 4.01//EN") && strings.Contains(systemID, "strict.dtd") {
					result.HTMLVersion = "HTML 4.01 Strict"
				} else if strings.Contains(publicID, "HTML 4.01 Transitional//EN") && strings.Contains(systemID, "loose.dtd") {
					result.HTMLVersion = "HTML 4.01 Transitional"
				} else if publicID != "" {
					result.HTMLVersion = "Unknown HTML (with Public ID)"
				} else {
					result.HTMLVersion = "HTML (Unknown Version)"
				}
			} else if doctypeName != "" { // A doctype was declared, but not 'html' (e.g., 'svg', 'math', or custom 'foo')
				result.HTMLVersion = "Unknown Doctype (" + doctypeName + ")"
			}
			// If result.HTMLVersion is still empty, the fallback after f(doc) will handle it.
			slog.Debug("Determined HTML version (during traversal)", "version", result.HTMLVersion)
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)

	// Fallback for HTML Version if not set during traversal
	if result.HTMLVersion == "" {
		slog.Debug("HTMLVersion not set during traversal, applying fallback.")
		result.HTMLVersion = "Unknown or No Doctype"
	}
	slog.Info("Final HTML version determined", "version", result.HTMLVersion)

	// --- 6. Inaccessible Links Check (Concurrent) ---
	if len(linksToTest) > 0 {
		slog.Debug("Checking accessibility for links", "count", len(linksToTest))
		result.InaccessibleLinks = checkLinkAccessibility(linksToTest)
		slog.Info("Link accessibility check complete", "inaccessible_count", len(result.InaccessibleLinks))
	} else {
		slog.Debug("No links found to check for accessibility.")
	}

	return result, nil
}

// detectLoginForm checks if a given form node seems to be a login form
func detectLoginForm(formNode *html.Node) bool {
	var hasPasswordInput bool
	var hasTextLikeInputNamedForLogin bool // Specifically for username, email, generic "login", "pass"
	var hasPinInput bool                   // Specifically for inputs named "pin" or similar
	var hasSubmitButton bool

	var f func(*html.Node)
	f = func(node *html.Node) {
		if node.Type == html.ElementNode {
			if node.DataAtom == atom.Input {
				isPassword := false
				isTextLike := false
				isTel := false
				isNumber := false
				nameAttr := ""

				for _, attr := range node.Attr {
					if attr.Key == "type" {
						typeVal := strings.ToLower(attr.Val)
						switch typeVal {
						case "password":
							isPassword = true
						case "text", "email":
							isTextLike = true
						case "tel":
							isTel = true
						case "number":
							isNumber = true
						}
					}
					if attr.Key == "name" {
						nameAttr = strings.ToLower(attr.Val)
					}
				}

				if isPassword {
					hasPasswordInput = true
				}
				if isTextLike { // For username, email
					if strings.Contains(nameAttr, "user") ||
						strings.Contains(nameAttr, "email") ||
						strings.Contains(nameAttr, "login") ||
						strings.Contains(nameAttr, "pass") { // pass for password or passphrase as a text field
						hasTextLikeInputNamedForLogin = true
					}
				}
				// Check for PIN specifically. A PIN field could be text, tel, or number type.
				if (isTextLike || isTel || isNumber || isPassword /* some PINs use type=password */) && strings.Contains(nameAttr, "pin") {
					hasPinInput = true
				}

			} else if node.DataAtom == atom.Button {
				typeAttr := ""
				isSubmit := false
				for _, attr := range node.Attr {
					if attr.Key == "type" {
						typeAttr = strings.ToLower(attr.Val)
						break
					}
				}
				if typeAttr == "submit" || typeAttr == "" { // Default button type in a form is submit
					isSubmit = true
				}
				// Could also check button text content like "Login", "Sign In", "Enter"
				if isSubmit {
					hasSubmitButton = true
				}
			}
		}
		for c := node.FirstChild; c != nil && !hasSubmitButton; c = c.NextSibling { // Optimization: if submit found, maybe stop early for this check
			f(c)
		}
	}
	f(formNode) // Traverse children of the form node

	slog.Debug("Login form detection details",
		"hasTextLikeInputNamedForLogin", hasTextLikeInputNamedForLogin,
		"hasPasswordInput", hasPasswordInput,
		"hasPinInput", hasPinInput,
		"hasSubmitButton", hasSubmitButton)

	// Criteria for a login form:
	// 1. Standard: Username/Email like input + Password input + Submit button
	isUserPassForm := hasTextLikeInputNamedForLogin && hasPasswordInput && hasSubmitButton
	// 2. PIN Form: A PIN-named input + Submit button
	isPinForm := hasPinInput && hasSubmitButton

	return isUserPassForm || isPinForm
}

// checkLinkAccessibility checks a list of URLs concurrently
func checkLinkAccessibility(links []string) []InaccessibleLinkInfo {
	var inaccessible []InaccessibleLinkInfo
	if len(links) == 0 {
		return inaccessible
	}

	var wg sync.WaitGroup
	var mu sync.Mutex

	concurrencyLimit := 10
	semaphore := make(chan struct{}, concurrencyLimit)

	httpClient := &http.Client{
		Timeout: 10 * time.Second,
	}

	for _, link := range links {
		wg.Add(1)
		semaphore <- struct{}{}

		go func(l string) {
			defer wg.Done()
			defer func() { <-semaphore }()

			slog.Debug("Checking link accessibility", "url", l)
			req, err := http.NewRequest(http.MethodHead, l, nil)
			if err != nil {
				mu.Lock()
				inaccessible = append(inaccessible, InaccessibleLinkInfo{URL: l, Error: "Failed to create request: " + err.Error()})
				mu.Unlock()
				return
			}
			req.Header.Set("User-Agent", "WebAnalyzerBot/1.0 (+http://example.com/bot)")

			resp, err := httpClient.Do(req)
			statusCode := 0
			if err != nil {
				if urlErr, ok := err.(*url.Error); ok {
					if strings.Contains(strings.ToLower(urlErr.Error()), "timeout") || strings.Contains(strings.ToLower(urlErr.Error()), "refused") {
						mu.Lock()
						inaccessible = append(inaccessible, InaccessibleLinkInfo{URL: l, StatusCode: 0, Error: urlErr.Error()})
						mu.Unlock()
						return
					}
				}
				// Try GET if HEAD fails (could be 405 or other method not allowed)
				reqGet, errGet := http.NewRequest(http.MethodGet, l, nil)
				if errGet != nil {
					mu.Lock()
					inaccessible = append(inaccessible, InaccessibleLinkInfo{URL: l, Error: "Failed to create GET request: " + errGet.Error()})
					mu.Unlock()
					return
				}
				reqGet.Header.Set("User-Agent", "WebAnalyzerBot/1.0 (+http://example.com/bot)")
				respGet, errGet := httpClient.Do(reqGet)
				if errGet != nil {
					if urlErr, ok := errGet.(*url.Error); ok {
						mu.Lock()
						inaccessible = append(inaccessible, InaccessibleLinkInfo{URL: l, StatusCode: 0, Error: urlErr.Error()})
						mu.Unlock()
						return
					}
					mu.Lock()
					inaccessible = append(inaccessible, InaccessibleLinkInfo{URL: l, Error: errGet.Error()})
					mu.Unlock()
					return
				}
				defer respGet.Body.Close()
				if respGet.StatusCode >= 400 {
					mu.Lock()
					inaccessible = append(inaccessible, InaccessibleLinkInfo{URL: l, StatusCode: respGet.StatusCode, Error: respGet.Status})
					mu.Unlock()
				}
				return
			}
			defer resp.Body.Close()
			statusCode = resp.StatusCode
			if statusCode == 405 {
				// Retry with GET if HEAD is not allowed
				reqGet, errGet := http.NewRequest(http.MethodGet, l, nil)
				if errGet != nil {
					mu.Lock()
					inaccessible = append(inaccessible, InaccessibleLinkInfo{URL: l, Error: "Failed to create GET request: " + errGet.Error()})
					mu.Unlock()
					return
				}
				reqGet.Header.Set("User-Agent", "WebAnalyzerBot/1.0 (+http://example.com/bot)")
				respGet, errGet := httpClient.Do(reqGet)
				if errGet != nil {
					if urlErr, ok := errGet.(*url.Error); ok {
						mu.Lock()
						inaccessible = append(inaccessible, InaccessibleLinkInfo{URL: l, StatusCode: 0, Error: urlErr.Error()})
						mu.Unlock()
						return
					}
					mu.Lock()
					inaccessible = append(inaccessible, InaccessibleLinkInfo{URL: l, Error: errGet.Error()})
					mu.Unlock()
					return
				}
				defer respGet.Body.Close()
				if respGet.StatusCode >= 400 {
					mu.Lock()
					inaccessible = append(inaccessible, InaccessibleLinkInfo{URL: l, StatusCode: respGet.StatusCode, Error: respGet.Status})
					mu.Unlock()
				}
				return
			}
			if statusCode >= 400 {
				mu.Lock()
				inaccessible = append(inaccessible, InaccessibleLinkInfo{URL: l, StatusCode: statusCode, Error: resp.Status})
				mu.Unlock()
			}
		}(link)
	}

	wg.Wait()
	return inaccessible
}

package analyzer

import (
	"fmt"
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
	resp, err := http.Get(pageURL)
	if err != nil {
		if urlErr, ok := err.(*url.Error); ok {
			// This error is more likely a network issue (DNS, connection refused).
			return nil, &AnalysisError{Message: fmt.Sprintf("Failed to fetch URL: %v", urlErr), StatusCode: 0}
		}
		return nil, &AnalysisError{Message: fmt.Sprintf("Failed to fetch URL: %v", err), StatusCode: 0}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 { // Handle HTTP error statuses explicitly
		return nil, &AnalysisError{
			Message:    fmt.Sprintf("URL returned HTTP error: %s", resp.Status),
			StatusCode: resp.StatusCode,
		}
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(strings.ToLower(contentType), "text/html") {
		return nil, &AnalysisError{
			Message:    fmt.Sprintf("URL is not an HTML page. Content-Type: %s", contentType),
			StatusCode: resp.StatusCode, // It's a valid response, but not HTML
		}
	}

	doc, err := html.Parse(resp.Body)
	if err != nil {
		return nil, &AnalysisError{Message: fmt.Sprintf("Failed to parse HTML: %v", err), StatusCode: resp.StatusCode}
	}

	result := &AnalysisResult{
		HeadingsCount: make(map[string]int),
	}

	var baseDomain *url.URL
	baseDomain, err = url.Parse(pageURL)
	if err != nil {
		// Should not happen if initial URL validation was good, but good to handle
		return nil, &AnalysisError{Message: fmt.Sprintf("Failed to parse base URL for link analysis: %v", err), StatusCode: resp.StatusCode}
	}

	var linksToTest []string

	// Traverse the HTML tree
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode {
			// --- 1. Page Title ---
			if n.DataAtom == atom.Title && n.FirstChild != nil {
				result.PageTitle = strings.TrimSpace(n.FirstChild.Data)
			}

			// --- 2. Headings Count ---
			switch n.DataAtom {
			case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
				// n.Data will be the tag name like "h1", "h2", etc.
				result.HeadingsCount[n.Data]++
				fmt.Printf("DEBUG: Added heading %s, current count: %d\n", n.Data, result.HeadingsCount[n.Data])
			}

			// --- 3. Links Count ---
			if n.DataAtom == atom.A {
				for _, attr := range n.Attr {
					if attr.Key == "href" {
						linkURL := strings.TrimSpace(attr.Val)
						if linkURL == "" || strings.HasPrefix(linkURL, "#") || strings.HasPrefix(strings.ToLower(linkURL), "javascript:") || strings.HasPrefix(strings.ToLower(linkURL), "mailto:") {
							continue // Skip empty, fragment, javascript, or mailto links
						}

						// Resolve relative URLs to absolute
						absoluteLink, err := baseDomain.Parse(linkURL)
						if err != nil {
							// If resolving fails, treat as an inaccessible link
							// For now, we'll just skip it or log
							fmt.Printf("Could not parse link %s: %v\n", linkURL, err)
							continue
						}
						linkStr := absoluteLink.String()

						if absoluteLink.Host == baseDomain.Host {
							result.InternalLinksCount++
						} else {
							result.ExternalLinksCount++
						}
						linksToTest = append(linksToTest, linkStr)
						break
					}
				}
			}

			// --- 4. Login Form Detection (Basic Heuristics) ---
			if n.DataAtom == atom.Form {
				result.ContainsLoginForm = result.ContainsLoginForm || detectLoginForm(n) // Check if already true
			}
		} else if n.Type == html.DoctypeNode {
			// --- 5. HTML Version (Check based on Doctype) ---
			if strings.EqualFold(n.Data, "html") {
				// Check for HTML5 Doctype: <!DOCTYPE html>
				publicID := ""
				systemID := ""
				for _, attr := range n.Attr {
					if attr.Key == "public" {
						publicID = attr.Val
					} else if attr.Key == "system" {
						systemID = attr.Val
					}
				}

				if publicID == "" && systemID == "" {
					result.HTMLVersion = "HTML5"
				} else if strings.Contains(publicID, "XHTML 1.0 Strict") {
					result.HTMLVersion = "XHTML 1.0 Strict"
				} else if strings.Contains(publicID, "XHTML 1.0 Transitional") {
					result.HTMLVersion = "XHTML 1.0 Transitional"
				} else if strings.Contains(publicID, "HTML 4.01 Strict") {
					result.HTMLVersion = "HTML 4.01 Strict"
				} else if strings.Contains(publicID, "HTML 4.01 Transitional") {
					result.HTMLVersion = "HTML 4.01 Transitional"
				} else {
					result.HTMLVersion = "Unknown"
				}
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)

	if result.HTMLVersion == "" {
		for c := doc.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.DoctypeNode {

				result.HTMLVersion = "Unknown or No Doctype"
				break
			}

		}
	}

	// --- 6. Inaccessible Links Check (Concurrent) ---
	result.InaccessibleLinks = checkLinkAccessibility(linksToTest)

	return result, nil
}

// detectLoginForm checks if a given form node seems to be a login form
func detectLoginForm(n *html.Node) bool {
	var hasPasswordInput bool
	var hasTextInput bool // For username, email, or PIN
	var hasSubmitButton bool

	var f func(*html.Node)
	f = func(node *html.Node) {
		if node.Type == html.ElementNode {
			if node.DataAtom == atom.Input {
				isPassword := false
				isTextLike := false // for text, email, PIN
				nameAttr := ""

				for _, attr := range node.Attr {
					if attr.Key == "type" {
						if strings.ToLower(attr.Val) == "password" {
							isPassword = true
						} else if strings.ToLower(attr.Val) == "text" ||
							strings.ToLower(attr.Val) == "email" ||
							strings.ToLower(attr.Val) == "tel" || // PIN might use 'tel'
							strings.ToLower(attr.Val) == "number" { // PIN might use 'number'
							isTextLike = true
						}
					}
					if attr.Key == "name" {
						nameAttr = strings.ToLower(attr.Val)
					}
				}

				if isPassword {
					hasPasswordInput = true
				}
				if isTextLike {
					// Check for common names associated with login
					if strings.Contains(nameAttr, "user") ||
						strings.Contains(nameAttr, "email") ||
						strings.Contains(nameAttr, "login") ||
						strings.Contains(nameAttr, "pass") || // For "passphrase" or similar
						strings.Contains(nameAttr, "pin") {
						hasTextInput = true
					}
				}
			} else if node.DataAtom == atom.Button {
				typeAttr := ""
				for _, attr := range node.Attr {
					if attr.Key == "type" {
						typeAttr = strings.ToLower(attr.Val)
						break
					}
				}
				if typeAttr == "submit" || typeAttr == "" {
					// Default button type in a form is submit
					hasSubmitButton = true
				}
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(n) // Traverse children of the form node

	fmt.Printf("hasTextInput: %t, hasPasswordInput: %t, hasSubmitButton: %t", hasTextInput, hasPasswordInput, hasSubmitButton)
	return (hasPasswordInput && hasSubmitButton) || (hasPasswordInput && hasTextInput && hasSubmitButton) || (hasTextInput && strings.Contains(n.Data, "pin") && hasSubmitButton) // crude pin check
}

// checkLinkAccessibility checks a list of URLs concurrently
func checkLinkAccessibility(links []string) []InaccessibleLinkInfo {
	var inaccessible []InaccessibleLinkInfo
	var wg sync.WaitGroup // WaitGroup waits for a collection of goroutines to finish
	// Mutex to protect concurrent writes to the 'inaccessible' slice
	var mu sync.Mutex

	// Limit concurrency to avoid overwhelming the network or server
	concurrencyLimit := 10
	semaphore := make(chan struct{}, concurrencyLimit) // A channel used as a semaphore

	for _, link := range links {
		wg.Add(1)               // Increment the WaitGroup counter
		semaphore <- struct{}{} // Acquire a slot in the semaphore

		go func(l string) {
			defer wg.Done()                // Decrement counter when goroutine finishes
			defer func() { <-semaphore }() // Release the slot

			// Use HEAD request to be lighter, fallback to GET if HEAD is not allowed/fails
			client := http.Client{Timeout: 10 * time.Second} // Set a timeout
			req, err := http.NewRequest(http.MethodHead, l, nil)
			if err != nil {
				mu.Lock()
				inaccessible = append(inaccessible, InaccessibleLinkInfo{URL: l, Error: "Failed to create request: " + err.Error()})
				mu.Unlock()
				return
			}

			resp, err := client.Do(req)
			statusCode := 0 // Default for non-HTTP errors

			if err != nil {
				// Network error (DNS, connection refused, timeout)
				mu.Lock()
				inaccessible = append(inaccessible, InaccessibleLinkInfo{URL: l, Error: err.Error()})
				mu.Unlock()
				return
			}
			defer resp.Body.Close()
			statusCode = resp.StatusCode

			// Consider any 2xx or 3xx status as accessible for this check
			// Strict check: if resp.StatusCode < 200 || resp.StatusCode >= 400 {
			if resp.StatusCode >= 400 { // 4xx and 5xx are definitely problems
				mu.Lock()
				inaccessible = append(inaccessible, InaccessibleLinkInfo{URL: l, StatusCode: statusCode, Error: resp.Status})
				mu.Unlock()
			}
		}(link)
	}

	wg.Wait() // Wait for all goroutines to complete
	return inaccessible
}

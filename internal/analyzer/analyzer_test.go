// analyzer_test.go
package analyzer

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"testing"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// starting point for the test suite
func TestMain(m *testing.M) {
	h := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug, AddSource: true})
	slog.SetDefault(slog.New(h))

	os.Exit(m.Run())
}

// Helper to create a mock HTTP server for testing
func newMockServer(handler http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(handler)
}

// TestFetchAndAnalyze_Full functionality
func TestFetchAndAnalyze_Full(t *testing.T) {
	// Mock server for links that should be accessible
	accessibleLinkServer := newMockServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "Accessible external content")
	})
	defer accessibleLinkServer.Close()

	// Mock server for example.com to ensure it's accessible during the test
	exampleComServer := newMockServer(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/external" { // Path used in the main test HTML
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "Mocked example.com/external content")
			return
		}
		http.NotFound(w, r)
	})
	defer exampleComServer.Close()

	// Mock server for the main page being analyzed
	mainServer := newMockServer(func(w http.ResponseWriter, r *http.Request) {
		htmlContent := fmt.Sprintf(`
			<!DOCTYPE html>
			<html>
			<head>
				<title>Test Page Title</title>
				<link rel="stylesheet" href="/style.css"> </head>
			<body>
				<h1>Main Heading</h1>
				<h2>Sub Heading</h2>
				<p>Some content.</p>
				<a href="/internal/page1">Internal Link 1</a> <a href="%s/external">External Link 1</a> <a href="%s/accessible-external">Accessible External Link</a> <a href="http://localhost:12345/definitely-broken-link">Broken Link</a> <form action="/login" method="post">
					<input type="text" name="username" />
					<input type="password" name="password" />
					<button type="submit">Login</button>
				</form>
				<a href="mailto:test@example.com">Mailto link</a>
				<a href="#">Fragment link</a>
				<a href="javascript:void(0)">JS link</a>
			</body>
			</html>
		`, exampleComServer.URL, accessibleLinkServer.URL)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, htmlContent)
	})
	defer mainServer.Close()

	result, err := FetchAndAnalyze(mainServer.URL)
	if err != nil {
		t.Fatalf("FetchAndAnalyze failed unexpectedly: %v", err)
	}

	// 1. HTML Version
	if result.HTMLVersion != "HTML5" {
		t.Errorf("Expected HTMLVersion 'HTML5', got '%s'", result.HTMLVersion)
	}

	// 2. Page Title
	if result.PageTitle != "Test Page Title" {
		t.Errorf("Expected PageTitle 'Test Page Title', got '%s'", result.PageTitle)
	}

	// 3. Headings Count
	expectedHeadings := map[string]int{"h1": 1, "h2": 1}
	if len(result.HeadingsCount) != len(expectedHeadings) {
		t.Errorf("Expected %d heading types, got %d. Headings: %v", len(expectedHeadings), len(result.HeadingsCount), result.HeadingsCount)
	}
	for level, count := range expectedHeadings {
		if result.HeadingsCount[level] != count {
			t.Errorf("Expected %d %s headings, got %d", count, level, result.HeadingsCount[level])
		}
	}

	// 4. Links Count
	// Internal: /style.css (from <link>), /internal/page1 (from <a>)
	// External: example.com/external, accessibleLinkServerURL/accessible-external, definitely-broken-link
	if result.InternalLinksCount != 2 { // UPDATED EXPECTATION
		t.Errorf("Expected 2 internal links, got %d", result.InternalLinksCount)
	}
	if result.ExternalLinksCount != 3 {
		t.Errorf("Expected 3 external links, got %d", result.ExternalLinksCount)
	}

	// 5. Inaccessible Links (Only "Broken Link" should be inaccessible now)
	if len(result.InaccessibleLinks) != 1 {
		t.Errorf("Expected 1 inaccessible link, got %d. Links: %+v", len(result.InaccessibleLinks), result.InaccessibleLinks)
	} else {
		foundBroken := false
		for _, url := range result.InaccessibleLinks {
			if strings.Contains(url, "definitely-broken-link") {
				foundBroken = true
				break
			}
		}
		if !foundBroken {
			t.Errorf("'definitely-broken-link' not found in inaccessible links list: %+v", result.InaccessibleLinks)
		}
	}

	// 6. Login Form
	if !result.ContainsLoginForm {
		t.Errorf("Expected ContainsLoginForm to be true, but got false")
	}
}

func TestFetchAndAnalyze_ErrorHandling(t *testing.T) {
	t.Run("NonExistentServer", func(t *testing.T) {
		_, err := FetchAndAnalyze("http://localhost:9999/nonexistent")
		if err == nil {
			t.Fatal("Expected an error for a non-existent server, got nil")
		}
		ae, ok := err.(*AnalysisError)
		if !ok {
			t.Fatalf("Expected error of type *AnalysisError, got %T", err)
		}
		if ae.StatusCode != 0 {
			t.Errorf("Expected StatusCode 0 for network error, got %d", ae.StatusCode)
		}
	})

	t.Run("HTTPErrorStatus", func(t *testing.T) {
		server := newMockServer(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Not Found", http.StatusNotFound)
		})
		defer server.Close()

		_, err := FetchAndAnalyze(server.URL)
		if err == nil {
			t.Fatal("Expected an error for HTTP 404 status, got nil")
		}
		ae, ok := err.(*AnalysisError)
		if !ok {
			t.Fatalf("Expected error of type *AnalysisError, got %T", err)
		}
		if ae.StatusCode != http.StatusNotFound {
			t.Errorf("Expected StatusCode %d, got %d", http.StatusNotFound, ae.StatusCode)
		}
	})

	t.Run("NonHTMLContent", func(t *testing.T) {
		server := newMockServer(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, `{"message": "this is json"}`)
		})
		defer server.Close()

		_, err := FetchAndAnalyze(server.URL)
		if err == nil {
			t.Fatal("Expected an error for non-HTML content, got nil")
		}
		ae, ok := err.(*AnalysisError)
		if !ok {
			t.Fatalf("Expected error of type *AnalysisError, got %T", err)
		}
		if !strings.Contains(ae.Message, "URL is not an HTML page") {
			t.Errorf("Expected error message to contain 'URL is not an HTML page', got '%s'", ae.Message)
		}
	})
}

func TestDetectLoginForm(t *testing.T) {
	testCases := []struct {
		name     string
		html     string
		expected bool
	}{
		{"NoForm", `<body></body>`, false},
		{"SimpleLoginForm", `<body><form><input type="text" name="username"><input type="password" name="password"><button type="submit">Login</button></form></body>`, true},
		{"LoginFormWithEmail", `<body><form><input type="email" name="email"><input type="password" name="pwd"><button>Sign In</button></form></body>`, true},
		{"PinFormWithNameNumberType", `<body><form><input type="number" name="pin"><button type="submit">Enter</button></form></body>`, true},     // Should be true
		{"PinFormWithNameTelType", `<body><form><input type="tel" name="user_pin_code"><button type="submit">Enter</button></form></body>`, true}, // Should be true
		{"PinFormWithNameTextType", `<body><form><input type="text" name="mypin"><button type="submit">Go</button></form></body>`, true},          // Should be true
		{"NonLoginForm", `<body><form><input type="text" name="search"><button type="submit">Search</button></form></body>`, false},
		{"FormWithPasswordOnly", `<body><form><input type="password" name="secret"><button type="submit">Go</button></form></body>`, false}, // Should be false with refined logic
		{"FormWithTextOnlyNoPinName", `<body><form><input type="text" name="user"><button type="submit">Go</button></form></body>`, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			doc, _ := html.Parse(strings.NewReader(fmt.Sprintf("<html><body>%s</body></html>", tc.html))) // Wrap in html & body for robustness
			var formNode *html.Node
			var findForm func(*html.Node)
			findForm = func(n *html.Node) {
				if n.Type == html.ElementNode && n.DataAtom == atom.Form {
					formNode = n
					return
				}
				for c := n.FirstChild; c != nil && formNode == nil; c = c.NextSibling {
					findForm(c)
				}
			}
			findForm(doc)

			var found bool
			if formNode != nil {
				found = detectLoginForm(formNode)
			} else if tc.expected == true { // If we expect a login form, a form node should exist
				t.Fatalf("detectLoginForm for '%s': expected a form to be found, but it was not", tc.name)
			}

			if found != tc.expected {
				t.Errorf("detectLoginForm for '%s': expected %v, got %v", tc.name, tc.expected, found)
			}
		})
	}
}

func TestCheckLinkAccessibility(t *testing.T) {
	okServer := newMockServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	defer okServer.Close()

	notFoundServer := newMockServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer notFoundServer.Close()

	timeoutServer := newMockServer(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(12 * time.Second) // Client timeout is 10s
		w.WriteHeader(http.StatusOK)
	})
	defer timeoutServer.Close()

	// Server that disallows HEAD but allows GET
	headFailGetOkServer := newMockServer(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed) // Or just a network error by closing connection
			return
		}
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "GET success")
			return
		}
		http.Error(w, "Should not happen", http.StatusInternalServerError)
	})
	defer headFailGetOkServer.Close()

	links := []string{
		okServer.URL + "/good",                // Accessible
		notFoundServer.URL + "/bad",           // Inaccessible (404)
		"http://localhost:12347/unreachable",  // Inaccessible (connection refused)
		timeoutServer.URL + "/timeout",        // Inaccessible (timeout)
		headFailGetOkServer.URL + "/headfail", // Should be accessible via GET retry
	}

	inaccessibleLinks := checkLinkAccessibility(links)

	// Expect /bad, /unreachable, /timeout to be inaccessible. /headfail should be accessible.
	if len(inaccessibleLinks) != 3 {
		t.Fatalf("Expected 3 inaccessible links, got %d. Details: %+v", len(inaccessibleLinks), inaccessibleLinks)
	}

	expectedInaccessible := map[string]bool{
		notFoundServer.URL + "/bad":          true,
		"http://localhost:12347/unreachable": true,
		timeoutServer.URL + "/timeout":       true,
	}
	foundInaccessibleCount := 0

	for _, url := range inaccessibleLinks {
		if _, ok := expectedInaccessible[url]; ok {
			foundInaccessibleCount++
		} else {
			t.Errorf("Unexpected link in inaccessible list: %s", url)
		}
	}
	if foundInaccessibleCount != len(expectedInaccessible) {
		t.Errorf("Mismatch in count of specific expected inaccessible links. Expected %d, found in list %d", len(expectedInaccessible), foundInaccessibleCount)
	}
}

func TestFetchAndAnalyze_HTMLVersions(t *testing.T) {
	testCases := []struct {
		name        string
		htmlContent string
		expectedVer string
	}{
		{"HTML5", `<!DOCTYPE html><html><head><title>T</title></head><body>H</body></html>`, "HTML5"},
		{"HTML401Strict", `<!DOCTYPE HTML PUBLIC "-//W3C//DTD HTML 4.01//EN" "http://www.w3.org/TR/html4/strict.dtd"><html></html>`, "HTML 4.01 Strict"},
		{"HTML401Transitional", `<!DOCTYPE HTML PUBLIC "-//W3C//DTD HTML 4.01 Transitional//EN" "http://www.w3.org/TR/html4/loose.dtd"><html></html>`, "HTML 4.01 Transitional"},
		{"XHTML10Strict", `<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Strict//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-strict.dtd"><html></html>`, "XHTML 1.0 Strict"},
		{"XHTML10Transitional", `<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Transitional//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-transitional.dtd"><html></html>`, "XHTML 1.0 Transitional"},
		{"NoDoctype", `<html><head><title>Test</title></head><body></body></html>`, "Unknown or No Doctype"},
		{"UnknownModernFoo", `<!DOCTYPE foo><html></html>`, "Unknown Doctype (foo)"},                                                       // UPDATED EXPECTATION
		{"HTMLWithPublicIDOnly", `<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML Basic 1.0//EN"><html></html>`, "Unknown HTML (with Public ID)"}, // Test new category
		{"HTML5WithExtraSpacesInDoctype", `<!DOCTYPE   html   ><html></html>`, "HTML5"},                                                    // html.Parse normalizes this
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			server := newMockServer(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprintln(w, tc.htmlContent)
			})
			defer server.Close()

			result, err := FetchAndAnalyze(server.URL)
			if err != nil {
				// For doctype tests, parsing should generally succeed unless HTML is severely malformed
				t.Fatalf("FetchAndAnalyze failed for doctype test '%s': %v", tc.name, err)
			}
			if result.HTMLVersion != tc.expectedVer {
				t.Errorf("For doctype test '%s': expected HTMLVersion '%s', got '%s'", tc.name, tc.expectedVer, result.HTMLVersion)
			}
		})
	}
}

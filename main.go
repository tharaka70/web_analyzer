package main

import (
	"fmt"
	"html/template"
	"log" // You can upgrade this to "log/slog" later as planned
	"net/http"
	"net/url"

	"github.com/tharaka70/web_analyzer/internal/analyzer"
)

// Global template variable
var tmpl *template.Template

// init function to parse templates on program startup
func init() {
	tmpl = template.Must(template.ParseGlob("templates/*.html"))
}

// This struct holds all data passed to HTML templates
type PageData struct {
	URL        string
	Error      string
	StatusCode int
	Analysis   *analyzer.AnalysisResult
}

// analyzeHandler processes the form submission and displays analysis results or errors
func analyzeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	submittedURL := r.FormValue("url")
	if submittedURL == "" {
		data := PageData{Error: "URL field cannot be empty."}
		templateErr := tmpl.ExecuteTemplate(w, "index.html", data)
		if templateErr != nil {
			log.Printf("Error rendering template for empty URL: %v", templateErr)
			http.Error(w, "Error rendering page", http.StatusInternalServerError)
		}
		return
	}

	// Validate the submitted URL
	parsedURL, parseErr := url.ParseRequestURI(submittedURL)
	if parseErr != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		data := PageData{Error: fmt.Sprintf("Invalid URL: %q. Must be a valid HTTP/HTTPS URL.", submittedURL)}
		templateErr := tmpl.ExecuteTemplate(w, "index.html", data)
		if templateErr != nil {
			log.Printf("Error rendering template for invalid URL: %v", templateErr)
			http.Error(w, "Error rendering page", http.StatusInternalServerError)
		}
		return
	}
	if parsedURL.Host == "" {
		data := PageData{Error: fmt.Sprintf("Invalid URL: %q. URL must include a host (e.g., example.com).", submittedURL)}
		templateErr := tmpl.ExecuteTemplate(w, "index.html", data)
		if templateErr != nil {
			log.Printf("Error rendering template for URL without host: %v", templateErr)
			http.Error(w, "Error rendering page", http.StatusInternalServerError)
		}
		return
	}

	log.Printf("Attempting to analyze URL: %s", parsedURL.String())

	// Perform the analysis by calling the function from the analyzer package
	analysisResult, analysisErr := analyzer.FetchAndAnalyze(parsedURL.String())

	if analysisErr != nil {
		log.Printf("Error analyzing URL %s: %v", parsedURL.String(), analysisErr)
		pageData := PageData{
			URL:   submittedURL, // Show the originally submitted URL
			Error: analysisErr.Error(),
		}
		// If the error is of the custom type, extract the status code
		if ae, ok := analysisErr.(*analyzer.AnalysisError); ok {
			pageData.StatusCode = ae.StatusCode
		}

		templateErr := tmpl.ExecuteTemplate(w, "index.html", pageData) // Show error on the index page
		if templateErr != nil {
			log.Printf("Error rendering template for analysis error: %v", templateErr)
			http.Error(w, "Error rendering page", http.StatusInternalServerError)
		}
		return
	}

	// If analysis is successful, prepare data for the results page
	log.Printf("Successfully analyzed URL: %s", parsedURL.String())
	pageData := PageData{
		URL:      submittedURL, // Show the originally submitted URL
		Analysis: analysisResult,
	}
	templateErr := tmpl.ExecuteTemplate(w, "results.html", pageData)
	if templateErr != nil {
		log.Printf("Error rendering results template: %v", templateErr)
		http.Error(w, "Error rendering page", http.StatusInternalServerError)
	}
}

// indexHandler serves the initial form page
func indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Execute the index.html template without any specific data
	err := tmpl.ExecuteTemplate(w, "index.html", nil)
	if err != nil {
		log.Printf("Error rendering index template: %v", err)
		http.Error(w, "Error rendering page", http.StatusInternalServerError)
	}
}

// main is the entry point of the application
func main() {
	// Serve static files (CSS) from the "static" directory
	fs := http.FileServer(http.Dir("static"))
	http.Handle("/static/", http.StripPrefix("/static/", fs))

	// Define application routes
	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/analyze", analyzeHandler)

	port := "8080"
	log.Printf("Server starting and listening on http://localhost:%s", port)

	// Start the HTTP server
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Could not start server: %s\n", err.Error())
	}
}

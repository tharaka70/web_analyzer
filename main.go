package main

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"os"

	"github.com/tharaka70/web_analyzer/internal/analyzer"
)

var logger *slog.Logger // Global logger instance

// Global template variable
var tmpl *template.Template

// init function to parse templates on program startup
func init() {
	// Initialize templates
	tmpl = template.Must(template.ParseGlob("templates/*.html"))

	// Initialize structured logger
	logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
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
			logger.Error("Error rendering template for empty URL:", "error", templateErr)
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
			logger.Error("Error rendering template for invalid URL:", "error", templateErr)
			http.Error(w, "Error rendering page", http.StatusInternalServerError)
		}
		return
	}
	if parsedURL.Host == "" {
		data := PageData{Error: fmt.Sprintf("Invalid URL: %q. URL must include a host (e.g., example.com).", submittedURL)}
		templateErr := tmpl.ExecuteTemplate(w, "index.html", data)
		if templateErr != nil {
			logger.Error("Error rendering template for URL without host", "error", templateErr)
			http.Error(w, "Error rendering page", http.StatusInternalServerError)
		}
		return
	}

	logger.Info("Attempting to analyze URL", "URL", parsedURL.String())

	// Perform the analysis by calling the function from the analyzer package
	analysisResult, analysisErr := analyzer.FetchAndAnalyze(parsedURL.String())

	if analysisErr != nil {
		logger.Error("Error analyzing URL %s: %v", parsedURL.String(), analysisErr)
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
			logger.Error("Error rendering template for analysis error:", "error", templateErr)
			http.Error(w, "Error rendering page", http.StatusInternalServerError)
		}
		return
	}

	// If analysis is successful, prepare data for the results page
	logger.Info("Successfully analyzed URL", "URL", parsedURL.String())
	pageData := PageData{
		URL:      submittedURL, // Show the originally submitted URL
		Analysis: analysisResult,
	}
	templateErr := tmpl.ExecuteTemplate(w, "results.html", pageData)
	if templateErr != nil {
		logger.Error("Error rendering results template:", "error", templateErr)
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
		logger.Error("Error rendering index template", "err", err)
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
	logger.Info("Server starting and listening on http://localhost:", "port", port)

	// Start the HTTP server
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		logger.Error("Could not start server:", "error", err.Error())
	}
}

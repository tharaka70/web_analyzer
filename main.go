package main

import (
	"html/template"
	"log"
	"net/http"
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

	port := "8080"
	log.Printf("Server starting and listening on http://localhost:%s", port)

	// Start the HTTP server
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Could not start server: %s\n", err.Error())
	}
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/option"
)

var templates *template.Template
var users = map[string]string{}    // In-memory user store (email:password)
var sessions = map[string]string{} // sessionID: email
var mu sync.Mutex
var db *firestore.Client

func main() {
	ctx := context.Background()
	templates = template.Must(template.ParseGlob("templates/*.html"))

	// Firestore init with service account
	opt := option.WithCredentialsFile("serviceAccountKey.json")
	client, err := firestore.NewClient(ctx, os.Getenv("GOOGLE_CLOUD_PROJECT"), opt)
	if err != nil {
		log.Fatalf("Failed to initialize Firestore: %v", err)
	}
	db = client
	defer db.Close()

	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	http.HandleFunc("/", homeHandler)
	http.HandleFunc("/signup", signupHandler)
	http.HandleFunc("/login", loginHandler)
	http.HandleFunc("/logout", logoutHandler)
	http.HandleFunc("/weather", weatherHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	fmt.Println("Server running on port", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	sessionID, err := r.Cookie("session_id")
	data := map[string]interface{}{
		"LoggedIn": false,
	}

	if err == nil {
		email, exists := sessions[sessionID.Value]
		if exists {
			data["LoggedIn"] = true
			data["Email"] = email
		}
	}

	templates.ExecuteTemplate(w, "home.html", data)
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		templates.ExecuteTemplate(w, "login.html", nil)
		return
	}

	if r.Method == http.MethodPost {
		email := r.FormValue("email")
		password := r.FormValue("password")

		mu.Lock()
		defer mu.Unlock()

		if storedPassword, exists := users[email]; !exists || storedPassword != password {
			http.Error(w, "Invalid email or password", http.StatusUnauthorized)
			return
		}

		// Create a session
		sessionID := fmt.Sprintf("%d", time.Now().UnixNano())
		sessions[sessionID] = email
		http.SetCookie(w, &http.Cookie{
			Name:  "session_id",
			Value: sessionID,
			Path:  "/",
		})

		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	sessionID, err := r.Cookie("session_id")
	if err == nil {
		mu.Lock()
		delete(sessions, sessionID.Value)
		mu.Unlock()
	}

	http.SetCookie(w, &http.Cookie{
		Name:   "session_id",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func signupHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		err := templates.ExecuteTemplate(w, "signup.html", nil)
		if err != nil {
			log.Printf("Error rendering signup template: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
		return
	}

	if r.Method == http.MethodPost {
		email := r.FormValue("email")
		password := r.FormValue("password")

		mu.Lock()
		defer mu.Unlock()

		if _, exists := users[email]; exists {
			http.Error(w, "User already exists", http.StatusConflict)
			return
		}

		users[email] = password
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

func weatherHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	city := r.URL.Query().Get("city")
	apiKey := os.Getenv("WEATHER_API_KEY")

	if city == "" || apiKey == "" {
		http.Error(w, "Missing city or API key", http.StatusBadRequest)
		return
	}

	url := fmt.Sprintf("http://api.weatherstack.com/current?access_key=%s&query=%s", apiKey, city)
	resp, err := http.Get(url)
	if err != nil {
		log.Printf("Error fetching weather data: %v", err) // Log the error
		http.Error(w, "Failed to fetch weather", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error reading API response body: %v", err)
		http.Error(w, "Failed to read weather data", http.StatusInternalServerError)
		return
	}
	log.Printf("API Response: %s", body)

	// Decode the response
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Error decoding weather data: %v", err)
		http.Error(w, "Failed to decode weather data", http.StatusInternalServerError)
		return
	}

	if result["error"] != nil {
		apiError := result["error"].(map[string]interface{})
		log.Printf("Error in weather API response: %v", apiError) // Log the error

		if apiError["type"] == "rate_limit_reached" {
			// Render an error message to the user
			templates.ExecuteTemplate(w, "error.html", map[string]string{
				"Message": "Weather API request limit reached. Please try again later.",
			})
			return
		}

		// Render a generic error message for other API errors
		templates.ExecuteTemplate(w, "error.html", map[string]string{
			"Message": "City not found. Please check your input.",
		})
		return
	}

	current := result["current"].(map[string]interface{})
	location := result["location"].(map[string]interface{})

	data := map[string]string{
		"City":        fmt.Sprintf("%s, %s", location["name"], location["country"]),
		"Temperature": fmt.Sprintf("%.1f°C", current["temperature"].(float64)),
		"Weather":     current["weather_descriptions"].([]interface{})[0].(string),
		"Humidity":    fmt.Sprintf("%v%%", current["humidity"]),
		"WindSpeed":   fmt.Sprintf("%v km/h", current["wind_speed"]),
	}

	log.Printf("Data passed to template: %+v", data)
	err = templates.ExecuteTemplate(w, "weather_result.html", data)
	if err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Failed to render weather data", http.StatusInternalServerError)
		return
	}
}

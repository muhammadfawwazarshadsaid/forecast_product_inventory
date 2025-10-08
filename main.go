package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

// Struktur request dari Apps Script
type ForecastRequest struct {
	SpreadsheetID string `json:"spreadsheetId"`
	SheetName     string `json:"sheetName"`
}

// Koneksi ke Google Sheets API
func getSheetsService() (*sheets.Service, error) {
	credsJSON := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS_JSON")
	if credsJSON == "" {
		return nil, fmt.Errorf("GOOGLE_APPLICATION_CREDENTIALS_JSON not set")
	}
	ctx := context.Background()
	return sheets.NewService(ctx, option.WithCredentialsJSON([]byte(credsJSON)))
}

// Forecast handler utama
func forecastHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		return
	}

	var req ForecastRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	srv, err := getSheetsService()
	if err != nil {
		http.Error(w, "Failed to connect Sheets API: "+err.Error(), 500)
		log.Println("Sheets connect error:", err)
		return
	}

	readRange := fmt.Sprintf("%s!B54:AM62", req.SheetName)
	writeRange := fmt.Sprintf("%s!AB54:AM62", req.SheetName)

	resp, err := srv.Spreadsheets.Values.Get(req.SpreadsheetID, readRange).Do()
	if err != nil {
		http.Error(w, "Failed read: "+err.Error(), 500)
		log.Println("Read error:", err)
		return
	}

	values := resp.Values
	if len(values) == 0 {
		http.Error(w, "No data", 400)
		return
	}

	// Dummy forecast logic (fill 2026 columns with last known value)
	for i := range values {
		for len(values[i]) < 45 { // pastikan cukup kolom
			values[i] = append(values[i], "")
		}
		last := values[i][35]
		for j := 36; j <= 45; j++ { // Jan–Dec 2026
			values[i][j] = last
		}
	}

	_, err = srv.Spreadsheets.Values.Update(req.SpreadsheetID, writeRange,
		&sheets.ValueRange{Values: values}).ValueInputOption("RAW").Do()
	if err != nil {
		http.Error(w, "Failed write: "+err.Error(), 500)
		log.Println("Write error:", err)
		return
	}

	log.Println("✅ Forecast updated for sheet:", req.SheetName)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "✅ Forecast updated"})
}

// Endpoint buat ngecek server aktif
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintln(w, "✅ Forecast API up and running")
}

func main() {
	http.HandleFunc("/forecast", forecastHandler)
	http.HandleFunc("/", healthHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	fmt.Printf("🚀 Server running on port %s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

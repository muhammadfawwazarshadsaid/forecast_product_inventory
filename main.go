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

type ForecastRequest struct {
	SpreadsheetID string `json:"spreadsheetId"`
	SheetName     string `json:"sheetName"`
}

func getSheetsService() (*sheets.Service, error) {
	credsJSON := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS_JSON")
	if credsJSON == "" {
		return nil, fmt.Errorf("GOOGLE_APPLICATION_CREDENTIALS_JSON not set")
	}
	ctx := context.Background()
	return sheets.NewService(ctx, option.WithCredentialsJSON([]byte(credsJSON)))
}

func forecastHandler(w http.ResponseWriter, r *http.Request) {
	var req ForecastRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	srv, err := getSheetsService()
	if err != nil {
		http.Error(w, "Failed to connect Sheets API: "+err.Error(), 500)
		return
	}

	readRange := fmt.Sprintf("%s!B54:AM62", req.SheetName)
	writeRange := fmt.Sprintf("%s!AB54:AL62", req.SheetName)

	resp, err := srv.Spreadsheets.Values.Get(req.SpreadsheetID, readRange).Do()
	if err != nil {
		http.Error(w, "Failed read: "+err.Error(), 500)
		return
	}

	values := resp.Values
	if len(values) == 0 {
		http.Error(w, "No data", 400)
		return
	}

	// Dummy forecast: just copy last known month to Jan–Nov 2026
	for i := range values {
		if len(values[i]) >= 36 { // enough columns
			last := values[i][35]
			for j := 34; j < 45; j++ { // Jan–Nov 2026
				if j < len(values[i]) {
					values[i][j] = last
				}
			}
		}
	}

	_, err = srv.Spreadsheets.Values.Update(req.SpreadsheetID, writeRange,
		& sheets.ValueRange{Values: values}).ValueInputOption("RAW").Do()
	if err != nil {
		http.Error(w, "Failed write: "+err.Error(), 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "✅ Forecast updated"})
}

func main() {
	http.HandleFunc("/forecast", forecastHandler)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	fmt.Printf("🚀 Server running on port %s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

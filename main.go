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
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		return
	}

	var req ForecastRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Println("📩 Forecast request received for:", req.SheetName)

	srv, err := getSheetsService()
	if err != nil {
		http.Error(w, "Failed to connect Sheets API: "+err.Error(), 500)
		log.Println("Sheets connect error:", err)
		return
	}

	readRange := fmt.Sprintf("%s!B54:AM62", req.SheetName)
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

	// Forecast logic: ambil nilai Dec-25 (kolom ke-36 -> index 35)
	for i, row := range values {
		for len(row) < 48 {
			row = append(row, "")
		}
		values[i] = row
		last := row[35]
		log.Printf("➡️ Row %d | Last known (Dec-25): %v\n", i+54, last)

		for j := 36; j <= 47; j++ { // AB–AM = Jan–Dec 2026
			values[i][j] = last
		}

		log.Printf("📊 Updated Row %d | Range AB–AM: %v\n", i+54, values[i][36:48])
	}

	// Ambil subset AB–AM buat update
	var updateRows [][]interface{}
	for _, row := range values {
		updateRows = append(updateRows, row[36:48])
	}

	writeRange := fmt.Sprintf("%s!AB54:AM62", req.SheetName)
	log.Println("📝 Writing to range:", writeRange)
	_, err = srv.Spreadsheets.Values.Update(req.SpreadsheetID, writeRange,
		&sheets.ValueRange{Values: updateRows}).ValueInputOption("RAW").Do()
	if err != nil {
		http.Error(w, "Failed write: "+err.Error(), 500)
		log.Println("Write error:", err)
		return
	}

	log.Println("✅ Forecast updated successfully for:", req.SheetName)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "✅ Forecast updated"})
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
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

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

	log.Println("📩 Dummy fill request received for:", req.SheetName)

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
		http.Error(w, "No data found in sheet", 400)
		return
	}

	for i, row := range values {
		if len(row) < 3 {
			continue
		}

		product := fmt.Sprintf("%v", row[0])
		remark := fmt.Sprintf("%v", row[1])

		// pastikan panjang row cukup
		for len(row) < 48 {
			row = append(row, "")
		}

		// 🧱 1️⃣ Skip TOTAL COGS (K-EUR)
		if product == "TOTAL" && remark == "COGS (K-EUR)" {
			log.Printf("🧱 Skipped TOTAL COGS (K-EUR) at row %d\n", i+54)
			continue
		}

		// 🧱 2️⃣ Khusus TOTAL DIN Yearly: isi Jan–Nov, biarkan Desember
		if product == "TOTAL" && remark == "TOTAL DIN Yearly" {
			for j := 36; j <= 46; j++ { // AB–AL (Jan–Nov)
				row[j] = 9999
			}
			// AM (47) dibiarkan
			log.Printf("🎯 TOTAL DIN Yearly updated (Jan–Nov dummy, Des untouched) row %d\n", i+54)
			values[i] = row
			continue
		}

		// 🧩 3️⃣ Semua remark lain: isi full AB–AM
		for j := 36; j <= 47; j++ {
			row[j] = 9999
		}
		log.Printf("✅ Row %d dummy filled (AB–AM)\n", i+54)
		values[i] = row
	}

	// Ambil subset kolom AB–AM untuk update
	var updateRows [][]interface{}
	for _, row := range values {
		updateRows = append(updateRows, row[36:48])
	}

	writeRange := fmt.Sprintf("%s!AB54:AM62", req.SheetName)
	log.Println("📝 Writing dummy values to:", writeRange)

	_, err = srv.Spreadsheets.Values.Update(req.SpreadsheetID, writeRange,
		&sheets.ValueRange{Values: updateRows}).ValueInputOption("RAW").Do()
	if err != nil {
		http.Error(w, "Failed write: "+err.Error(), 500)
		log.Println("Write error:", err)
		return
	}

	log.Println("✅ Dummy fill completed successfully")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "✅ Dummy fill completed"})
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

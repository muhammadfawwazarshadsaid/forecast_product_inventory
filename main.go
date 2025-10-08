package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"

	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

// ======================= Request/Response Types =======================

type ForecastRequest struct {
	SpreadsheetID      string   `json:"spreadsheetId"`
	SheetName          string   `json:"sheetName"`
	KnownCOGSTotal2026 *float64 `json:"knownCogsTotal2026,omitempty"` // optional; override
	KnownDINDec2026    *float64 `json:"knownDinDec2026,omitempty"`     // optional; override
}

type GeminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

// ======================= Utilities =======================

func getSheetsService() (*sheets.Service, error) {
	credsJSON := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS_JSON")
	if credsJSON == "" {
		return nil, fmt.Errorf("GOOGLE_APPLICATION_CREDENTIALS_JSON not set")
	}
	ctx := context.Background()
	return sheets.NewService(ctx, option.WithCredentialsJSON([]byte(credsJSON)))
}

func atof(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		if t == "" {
			return 0
		}
		f, _ := strconv.ParseFloat(t, 64)
		return f
	default:
		return 0
	}
}

func round2(x float64) float64 {
	return math.Round(x*100) / 100
}

func stripFence(s string) string {
	// remove ```json ... ``` fences if any
	if len(s) >= 7 && s[:7] == "```json" {
		s = s[7:]
	}
	if len(s) >= 3 && s[:3] == "```" {
		s = s[3:]
	}
	if n := len(s); n >= 3 && s[n-3:] == "```" {
		s = s[:n-3]
	}
	return s
}

func mustJSON(v interface{}) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

// ======================= Gemini Forecast (Pro -> Flash fallback) =======================

func callGeminiForecast(
	pastTotals map[string]map[string]float64,
	lastYearProfile map[string][]float64,
	cogs2026Total, dinDec2026 float64,
) (map[string]map[string]float64, error) {

	// Optional: allow env override (default try Pro -> fallback Flash)
	modelEnv := os.Getenv("GEMINI_MODEL") // e.g., "gemini-2.5-pro" or "gemini-2.5-flash"
	models := []string{"gemini-2.5-pro", "gemini-2.5-flash"}
	if modelEnv != "" {
		// still try a second model as a fallback if user set Pro explicitly
		if modelEnv == "gemini-2.5-pro" {
			models = []string{"gemini-2.5-pro", "gemini-2.5-flash"}
		} else {
			models = []string{modelEnv, "gemini-2.5-flash"}
		}
	}

	prompt := fmt.Sprintf(`You are a financial forecasting AI.

Given:
1) Per-remark yearly totals for 2024 and 2025 (K-EUR).
2) 2025 monthly share profile per remark (12 numbers summing ≈ 1.0).
3) Known total COGS for 2026 (K-EUR) = %.2f.
4) Known "TOTAL DIN Yearly" December 2026 (K-EUR) = %.2f (this cell must remain untouched; you only forecast Jan-Nov).

Task:
- Forecast Jan-Nov 2026 for each remark (K-EUR), following trend from 2024->2025 and 2025 monthly profile as prior for seasonality.
- Keep values smooth and non-negative. Avoid unrealistic spikes.
- Keep proportions versus COGS roughly consistent with past years (soft constraint).
- Return STRICT JSON only (no Markdown fences).
- JSON structure:
{
  "GIN (K-EUR)": {"Jan": n, "Feb": n, ..., "Nov": n},
  "GIT (K-EUR)": {...},
  "RM (K-EUR)": {...},
  "WIP (K-EUR)": {...},
  "FG (K-EUR)": {...},
  "DEP (K-EUR)": {...},
  "NIN TOTAL": {...},          // if present in input
  "TOTAL NIN Spot": {...},     // if present in input
  "TOTAL DIN Yearly": {...}    // Jan-Nov only; December is fixed externally
}

Yearly totals (K-EUR):
%s

2025 monthly share profile (12 numbers each remark, sum ≈ 1.0):
%s
`, cogs2026Total, dinDec2026, mustJSON(pastTotals), mustJSON(lastYearProfile))

	payload := map[string]interface{}{
		"contents": []map[string]interface{}{
			{"role": "user", "parts": []map[string]string{{"text": prompt}}},
		},
	}
	body, _ := json.Marshal(payload)

	var lastErr error

	for _, model := range models {
		url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", model)
		req, _ := http.NewRequest("POST", url, bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-goog-api-key", os.Getenv("GEMINI_API_KEY"))

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s request error: %v", model, err)
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			lastErr = fmt.Errorf("%s API returned %d", model, resp.StatusCode)
			log.Printf("⚠️ %s failed (%d), trying fallback...\n", model, resp.StatusCode)
			continue
		}

		var result GeminiResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			lastErr = fmt.Errorf("%s decode failed: %v", model, err)
			continue
		}
		if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
			lastErr = fmt.Errorf("%s no candidates", model)
			continue
		}

		text := stripFence(result.Candidates[0].Content.Parts[0].Text)

		out := map[string]map[string]float64{}
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			log.Println("⚠️ Gemini raw output:", text)
			lastErr = fmt.Errorf("%s output invalid JSON: %v", model, err)
			continue
		}

		log.Printf("✅ Forecast generated with %s\n", model)
		return out, nil
	}

	return nil, fmt.Errorf("all Gemini calls failed: %v", lastErr)
}

// ======================= /forecast Handler =======================

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

	if os.Getenv("GEMINI_API_KEY") == "" {
		http.Error(w, "GEMINI_API_KEY not set", 500)
		return
	}

	// Sheets
	srv, err := getSheetsService()
	if err != nil {
		http.Error(w, "Failed Sheets API: "+err.Error(), 500)
		return
	}

	// Baca range data target (sesuai layout lo)
	readRange := fmt.Sprintf("%s!B54:AM62", req.SheetName)
	resp, err := srv.Spreadsheets.Values.Get(req.SpreadsheetID, readRange).Do()
	if err != nil {
		http.Error(w, "Failed read: "+err.Error(), 500)
		return
	}
	values := resp.Values
	if len(values) == 0 {
		http.Error(w, "No data found", 400)
		return
	}

	// Indeks (relatif ke kolom B sebagai index 0)
	const (
		colJan26   = 26 // AB
		colNov26   = 36 // AL
		colDec26   = 37 // AM
		colStart24 = 2  // Jan-24
		colEnd24   = 13 // Dec-24
		colStart25 = 14 // Jan-25
		colEnd25   = 25 // Dec-25
	)

	// Siapkan data historis + profil 2025
	pastTotals := map[string]map[string]float64{} // remark -> {"2024": x, "2025": y}
	lastYearProfile := map[string][]float64{}     // remark -> 12 share (2025)
	var cogs2026Total float64                     // anchor total COGS 2026
	var dinDec2026 float64                        // anchor DIN Yearly Dec 2026

	for _, row := range values {
		if len(row) < 2 {
			continue
		}
		product := fmt.Sprintf("%v", row[0])
		remark := fmt.Sprintf("%v", row[1])

		for len(row) <= colDec26 {
			row = append(row, "")
		}

		// sum 2024/2025
		var y2024 float64
		tmp25 := make([]float64, 12)
		sum25 := 0.0

		for c := colStart24; c <= colEnd24; c++ {
			y2024 += atof(row[c])
		}
		for i, c := 0, colStart25; c <= colEnd25; i, c = i+1, c+1 {
			v := atof(row[c])
			tmp25[i] = v
			sum25 += v
		}

		prof25 := make([]float64, 12)
		if sum25 > 0 {
			for i := 0; i < 12; i++ {
				prof25[i] = tmp25[i] / sum25
			}
		} else {
			for i := 0; i < 12; i++ {
				prof25[i] = 1.0 / 12.0
			}
		}

		if remark != "" {
			pastTotals[remark] = map[string]float64{"2024": round2(y2024), "2025": round2(sum25)}
			lastYearProfile[remark] = prof25
		}

		// anchors
		if product == "TOTAL" && remark == "COGS (K-EUR)" && req.KnownCOGSTotal2026 == nil {
			sum26 := 0.0
			for c := colJan26; c <= colDec26; c++ {
				sum26 += atof(row[c])
			}
			cogs2026Total = round2(sum26)
		}
		if product == "TOTAL" && remark == "TOTAL DIN Yearly" && req.KnownDINDec2026 == nil {
			dinDec2026 = round2(atof(row[colDec26]))
		}
	}

	// override anchors kalau dikirim via request
	if req.KnownCOGSTotal2026 != nil {
		cogs2026Total = *req.KnownCOGSTotal2026
	}
	if req.KnownDINDec2026 != nil {
		dinDec2026 = *req.KnownDINDec2026
	}

	// (Opsional) RAG retrieval dari vector DB untuk konteks tambahan
	if db, err := getDB(); err == nil {
		if similar, err := searchSimilarRemarks(db, "COGS & DIN trend 2024 2025 finance forecast", 5); err == nil {
			log.Printf("🔍 VectorDB similar remarks: %d\n", len(similar))
			_ = similar // bisa di-inject ke prompt kalau mau (lihat vectorstore.go)
		}
	} else {
		log.Println("Skip vector DB (no DATABASE_URL)")
	}

	// Panggil Gemini (Pro → fallback Flash)
	aiResult, err := callGeminiForecast(pastTotals, lastYearProfile, cogs2026Total, dinDec2026)
	if err != nil {
		http.Error(w, "AI Forecast failed: "+err.Error(), 500)
		return
	}

	months := []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov"}

	// Tulis balik Jan-Nov (AB-AL); Des (AM) tetap
	for i, row := range values {
		if len(row) < 2 {
			continue
		}
		product := fmt.Sprintf("%v", row[0])
		remark := fmt.Sprintf("%v", row[1])

		if product == "TOTAL" && remark == "COGS (K-EUR)" {
			continue // anchor
		}
		if product == "TOTAL" && remark == "TOTAL DIN Yearly" {
			if aiRow, ok := aiResult[remark]; ok {
				for j, m := range months {
					row[colJan26+j] = round2(aiRow[m])
				}
				values[i] = row
			}
			continue
		}

		if aiRow, ok := aiResult[remark]; ok {
			for j, m := range months {
				row[colJan26+j] = round2(aiRow[m])
			}
			values[i] = row
		}
	}

	// Update balik (AB:AM)
	updateRows := make([][]interface{}, 0, len(values))
	for _, row := range values {
		for len(row) <= colDec26 {
			row = append(row, "")
		}
		chunk := make([]interface{}, 0, (colDec26-colJan26)+1)
		for c := colJan26; c <= colDec26; c++ {
			chunk = append(chunk, row[c])
		}
		updateRows = append(updateRows, chunk)
	}

	writeRange := fmt.Sprintf("%s!AB54:AM62", req.SheetName)
	_, err = srv.Spreadsheets.Values.Update(req.SpreadsheetID, writeRange,
		&sheets.ValueRange{Values: updateRows}).ValueInputOption("RAW").Do()
	if err != nil {
		http.Error(w, "Failed write: "+err.Error(), 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":          "✅ AI Forecast completed",
		"cogs_total_2026": fmt.Sprintf("%.2f", cogs2026Total),
		"din_dec_2026":    fmt.Sprintf("%.2f", dinDec2026),
	})
}

// ======================= Health =======================

func healthHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "✅ Forecast AI API up and running")
}

// ======================= main =======================

func main() {
	http.HandleFunc("/forecast", forecastHandler)
	http.HandleFunc("/train-embeddings", trainEmbeddingsHandler) // from trainer.go
	http.HandleFunc("/", healthHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("🚀 AI Forecast Server running on port %s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

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
	"time"

	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

// ======================= Globals & Structs =======================

var apiClient = &http.Client{
	Timeout: 30 * time.Second,
}

type ForecastRequest struct {
	SpreadsheetID      string   `json:"spreadsheetId"`
	SheetName          string   `json:"sheetName"`
	KnownCOGSTotal2026 *float64 `json:"knownCogsTotal2026,omitempty"`
	KnownDINDec2026    *float64 `json:"knownDinDec2026,omitempty"`
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

// ======================= Utility Functions =======================

func getSheetsService(ctx context.Context) (*sheets.Service, error) {
	credsJSON := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS_JSON")
	if credsJSON == "" {
		return nil, fmt.Errorf("env variable GOOGLE_APPLICATION_CREDENTIALS_JSON not set")
	}
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

// ======================= Gemini Forecast Function =======================

func callGeminiForecast(
	ctx context.Context,
	pastTotals map[string]map[string]float64,
	lastYearProfile map[string][]float64,
	cogs2026Total, dinDec2026 float64,
) (map[string]map[string]float64, error) {

	models := []string{"gemini-1.5-flash-latest", "gemini-1.5-pro-latest"}

	prompt := fmt.Sprintf(`You are an efficient financial forecasting AI.

Given:
1) 2024 & 2025 yearly totals (K-EUR): %s
2) 2025 monthly seasonality profile: %s
3) Known total COGS for 2026 (K-EUR): %.2f
4) Known "TOTAL DIN Yearly" for Dec 2026 (K-EUR): %.2f. This is a hard constraint. The sum of the 12 monthly "TOTAL NIN Spot" values must equal this number.

Task:
- Forecast Jan-Dec 2026 for all remarks based on trends and seasonality.
- Calculate "TOTAL NIN Spot" for all 12 months, then scale them proportionally so their sum exactly equals %.2f.
- Calculate "TOTAL DIN Yearly" as the correct cumulative sum of the scaled "TOTAL NIN Spot".
- Values must be non-negative and smooth.
- Return STRICT JSON only (no markdown).
- JSON structure: {"GIN (K-EUR)": {"Jan": n, ..., "Dec": n}, ...}
`, mustJSON(pastTotals), mustJSON(lastYearProfile), cogs2026Total, dinDec2026, dinDec2026)

	bodyReq := map[string]interface{}{
		"contents": []map[string]interface{}{
			{"role": "user", "parts": []map[string]string{{"text": prompt}}},
		},
	}
	body, _ := json.Marshal(bodyReq)

	var lastErr error
	for _, model := range models {
		// BUG FIX: Variabel 'model' ditambahkan kembali sebagai argumen untuk fmt.Sprintf
		url := fmt.Sprintf("[https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent](https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent)", model)
		
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(body))
		if err != nil {
			return nil, fmt.Errorf("failed to create request for model %s: %w", model, err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-goog-api-key", os.Getenv("GEMINI_API_KEY"))

		resp, err := apiClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s request failed: %w", model, err)
			log.Printf("⚠️ Model %s failed: %v. Trying next...", model, err)
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			lastErr = fmt.Errorf("%s API returned status %d", model, resp.StatusCode)
			log.Printf("⚠️ Model %s returned status %d. Trying next...", model, resp.StatusCode)
			continue
		}

		var result GeminiResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			lastErr = fmt.Errorf("%s decode failed: %w", model, err)
			continue
		}
		if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
			lastErr = fmt.Errorf("%s returned no candidates", model)
			continue
		}

		text := stripFence(result.Candidates[0].Content.Parts[0].Text)
		out := map[string]map[string]float64{}
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			log.Println("⚠️ Gemini raw output:", text)
			lastErr = fmt.Errorf("%s returned invalid JSON: %w", model, err)
			continue
		}

		log.Printf("✅ Forecast generated successfully with %s", model)
		return out, nil
	}
	return nil, fmt.Errorf("all Gemini models failed: %w", lastErr)
}

// ======================= Main Forecast Handler =======================

func forecastHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	if r.Method != http.MethodPost {
		http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		return
	}

	var reqData ForecastRequest
	if err := json.NewDecoder(r.Body).Decode(&reqData); err != nil {
		http.Error(w, "Bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	srv, err := getSheetsService(ctx)
	if err != nil {
		http.Error(w, "Sheets API init failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Println("Step 1: Reading data from Google Sheets...")
	readRange := fmt.Sprintf("%s!B54:AM63", reqData.SheetName)
	resp, err := srv.Spreadsheets.Values.Get(reqData.SpreadsheetID, readRange).Do()
	if err != nil {
		http.Error(w, "Failed to read from sheet: "+err.Error(), http.StatusInternalServerError)
		return
	}
	values := resp.Values
	if len(values) == 0 {
		http.Error(w, "No data found", http.StatusBadRequest)
		return
	}

	log.Println("Step 2: Processing historical data...")
	const (
		colJan26   = 26; colDec26   = 37
		colStart24 = 2;  colEnd24   = 13
		colStart25 = 14; colEnd25   = 25
	)

	pastTotals := map[string]map[string]float64{}
	lastYearProfile := map[string][]float64{}
	var cogs2026Total, dinDec2026 float64

	for _, row := range values {
		if len(row) < 2 { continue }
		remark := fmt.Sprintf("%v", row[1])
		for len(row) <= colDec26 { row = append(row, "") }

		var y2024, sum25 float64; tmp25 := make([]float64, 12)
		for c := colStart24; c <= colEnd24; c++ { y2024 += atof(row[c]) }
		for i, c := 0, colStart25; c <= colEnd25; i, c = i+1, c+1 { v := atof(row[c]); tmp25[i] = v; sum25 += v }
		
		prof25 := make([]float64, 12)
		if sum25 > 0 { for i := range prof25 { prof25[i] = tmp25[i] / sum25 }
		} else { for i := range prof25 { prof25[i] = 1.0 / 12.0 } }
		
		if remark != "" {
			pastTotals[remark] = map[string]float64{"2024": round2(y2024), "2025": round2(sum25)}
			lastYearProfile[remark] = prof25
		}
		
		if remark == "COGS (K-EUR)" && reqData.KnownCOGSTotal2026 == nil {
			for c := colJan26; c <= colDec26; c++ { cogs2026Total += atof(row[c]) }
			cogs2026Total = round2(cogs2026Total)
		}
		if remark == "TOTAL DIN Yearly" && reqData.KnownDINDec2026 == nil { dinDec2026 = round2(atof(row[colDec26])) }
	}

	if reqData.KnownCOGSTotal2026 != nil { cogs2026Total = *reqData.KnownCOGSTotal2026 }
	if reqData.KnownDINDec2026 != nil { dinDec2026 = *reqData.KnownDINDec2026 }

	log.Println("Step 3: Calling Gemini AI for forecast...")
	startTime := time.Now()
	aiResult, err := callGeminiForecast(ctx, pastTotals, lastYearProfile, cogs2026Total, dinDec2026)
	duration := time.Since(startTime)
	if err != nil {
		log.Printf("❌ Gemini AI forecast failed after %v: %v", duration, err)
		http.Error(w, "AI Forecast failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("✅ Gemini AI responded in %v", duration)

	log.Println("Step 4: Preparing data for update...")
	months := []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	for i, row := range values {
		if len(row) < 2 { continue }
		remark := fmt.Sprintf("%v", row[1])
		if remark == "COGS (K-EUR)" { continue }

		if aiRow, ok := aiResult[remark]; ok {
			for j, m := range months {
				if colJan26+j < len(row) { row[colJan26+j] = round2(aiRow[m]) }
			}
			values[i] = row
		}
	}

	updateRows := make([][]interface{}, 0, len(values))
	for _, row := range values { updateRows = append(updateRows, row[colJan26:colDec26+1]) }

	log.Println("Step 5: Writing results back to Google Sheets...")
	writeRange := fmt.Sprintf("%s!AB54:AM63", reqData.SheetName)
	_, err = srv.Spreadsheets.Values.Update(reqData.SpreadsheetID, writeRange,
		&sheets.ValueRange{Values: updateRows}).ValueInputOption("USER_ENTERED").Do()
	if err != nil {
		http.Error(w, "Failed to write to sheet: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":          "✅ AI Forecast completed and updated successfully!",
		"ai_call_duration": duration.String(),
	})
}

// ======================= Server Setup =======================

func healthHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "✅ Forecast AI API is up and running")
}

func main() {
	http.HandleFunc("/forecast", forecastHandler)
	http.HandleFunc("/", healthHandler)

	port := os.Getenv("PORT")
	if port == "" { port = "8080" }
	log.Printf("🚀 AI Forecast Server starting on port %s...", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("❌ Server failed to start: %v", err)
	}
}
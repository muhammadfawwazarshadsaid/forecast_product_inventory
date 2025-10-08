package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

type TrainRequest struct {
	SpreadsheetID string `json:"spreadsheetId"`
	SheetName     string `json:"sheetName"`
	Year          int    `json:"year"` // 2024 or 2025
}

func getSheetsServiceTrain() (*sheets.Service, error) {
	credsJSON := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS_JSON")
	if credsJSON == "" {
		return nil, fmt.Errorf("GOOGLE_APPLICATION_CREDENTIALS_JSON not set")
	}
	ctx := context.Background()
	return sheets.NewService(ctx, option.WithCredentialsJSON([]byte(credsJSON)))
}

func trainEmbeddingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		return
	}

	var req TrainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if os.Getenv("GEMINI_API_KEY") == "" {
		http.Error(w, "GEMINI_API_KEY not set", 500)
		return
	}

	db, err := getDB()
	if err != nil {
		http.Error(w, "DB connect error: "+err.Error(), 500)
		return
	}

	srv, err := getSheetsServiceTrain()
	if err != nil {
		http.Error(w, "Sheets error: "+err.Error(), 500)
		return
	}

	// Ambil semua baris remark untuk di-embed
	readRange := fmt.Sprintf("%s!B54:AM62", req.SheetName)
	resp, err := srv.Spreadsheets.Values.Get(req.SpreadsheetID, readRange).Do()
	if err != nil {
		http.Error(w, "Failed read: "+err.Error(), 500)
		return
	}
	values := resp.Values

	total := len(values)
	successCount := 0
	skipCount := 0
	failCount := 0

	log.Printf("📦 Start training embeddings: rows=%d, year=%d\n", total, req.Year)

	for _, row := range values {
		if len(row) < 2 {
			continue
		}
		product := fmt.Sprintf("%v", row[0])
		remark := fmt.Sprintf("%v", row[1])
		if product == "" || remark == "" {
			continue
		}

		// Cek duplikat biar ga dobel
		var exists bool
		if err := db.QueryRow(
			`SELECT EXISTS(SELECT 1 FROM remark_embeddings WHERE product=$1 AND remark=$2 AND year=$3)`,
			product, remark, req.Year,
		).Scan(&exists); err == nil && exists {
			skipCount++
			continue
		}

		// Text yang di-embed (sederhana & konsisten)
		text := fmt.Sprintf("%s %s (%d)", product, remark, req.Year)
		vec, err := getEmbedding(text)
		if err != nil {
			log.Printf("❌ Embed fail %s %s: %v", product, remark, err)
			failCount++
			continue
		}

		meta := map[string]interface{}{
			"product": product,
			"remark":  remark,
			"year":    req.Year,
			"time":    time.Now().Format(time.RFC3339),
			"source":  "sheets:" + req.SheetName,
		}

		if err := saveEmbedding(db, RemarkEmbedding{
			Product:   product,
			Remark:    remark,
			Year:      req.Year,
			Embedding: vec,
			Metadata:  meta,
		}); err != nil {
			log.Printf("⚠️ DB insert fail: %v", err)
			failCount++
			continue
		}

		successCount++
		log.Printf("✅ Embedded: %s | %s | %d", product, remark, req.Year)
	}

	res := map[string]interface{}{
		"status":    "completed",
		"sheet":     req.SheetName,
		"year":      req.Year,
		"total":     total,
		"success":   successCount,
		"skipped":   skipCount,
		"failed":    failCount,
		"timestamp": time.Now().Format(time.RFC3339),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)

	log.Printf("🎯 Train done: success=%d skipped=%d failed=%d\n", successCount, skipCount, failCount)
}

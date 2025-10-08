package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	_ "github.com/lib/pq"
)

// =============== Types ===============

type RemarkEmbedding struct {
	Product   string
	Remark    string
	Year      int
	Embedding []float64
	Metadata  map[string]interface{}
}

type SearchResult struct {
	Product string
	Remark  string
	Year    int
	Score   float64
}

// =============== DB Connect ===============

func getDB() (*sql.DB, error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return nil, fmt.Errorf("DATABASE_URL not set")
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		return nil, err
	}
	return db, db.Ping()
}

// =============== Embedding API (robust parser) ===============

func getEmbedding(text string) ([]float64, error) {
	if os.Getenv("GEMINI_API_KEY") == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY not set")
	}

	payload := map[string]interface{}{
		"model": "textembedding-gecko",
		"content": map[string]interface{}{
			"parts": []map[string]string{{"text": text}},
		},
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST",
		"https://generativelanguage.googleapis.com/v1beta/models/textembedding-gecko:embedContent",
		bytes.NewBuffer(body),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", os.Getenv("GEMINI_API_KEY"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Handle multiple possible response shapes
	var raw map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	// Try: {"embedding":{"values":[...]}}
	if emb, ok := raw["embedding"].(map[string]interface{}); ok {
		if vals, ok := emb["values"].([]interface{}); ok {
			return ifaceToFloatSlice(vals), nil
		}
	}

	// Try: {"embeddings":[{"values":[...]}]}
	if arr, ok := raw["embeddings"].([]interface{}); ok && len(arr) > 0 {
		if first, ok := arr[0].(map[string]interface{}); ok {
			if vals, ok := first["values"].([]interface{}); ok {
				return ifaceToFloatSlice(vals), nil
			}
		}
	}

	// Try: {"data":[{"embedding":{"values":[...]}}]}
	if data, ok := raw["data"].([]interface{}); ok && len(data) > 0 {
		if first, ok := data[0].(map[string]interface{}); ok {
			if emb, ok := first["embedding"].(map[string]interface{}); ok {
				if vals, ok := emb["values"].([]interface{}); ok {
					return ifaceToFloatSlice(vals), nil
				}
			}
		}
	}

	return nil, fmt.Errorf("unexpected embedding response shape: %v", mustJSON(raw))
}

func ifaceToFloatSlice(vals []interface{}) []float64 {
	out := make([]float64, 0, len(vals))
	for _, v := range vals {
		switch t := v.(type) {
		case float64:
			out = append(out, t)
		case json.Number:
			f, _ := t.Float64()
			out = append(out, f)
		default:
			// try to coerce
			b, _ := json.Marshal(t)
			var f float64
			_ = json.Unmarshal(b, &f)
			out = append(out, f)
		}
	}
	return out
}

// =============== Save / Search ===============

// Save embedding (cast parameter to vector)
func saveEmbedding(db *sql.DB, e RemarkEmbedding) error {
	metaJSON, _ := json.Marshal(e.Metadata)
	// Convert slice to pgvector literal "[v1,v2,...]"
	vecLit := fmt.Sprintf("[%s]", floatSliceToCSV(e.Embedding))

	_, err := db.Exec(`
		INSERT INTO remark_embeddings (product, remark, year, embedding, metadata)
		VALUES ($1, $2, $3, $4::vector, $5)
		ON CONFLICT DO NOTHING
	`, e.Product, e.Remark, e.Year, vecLit, string(metaJSON))
	return err
}

// KNN search (cosine distance). Score = 1 - distance (higher is better).
func searchSimilarRemarks(db *sql.DB, query string, limit int) ([]SearchResult, error) {
	vec, err := getEmbedding(query)
	if err != nil {
		return nil, err
	}
	vecLit := fmt.Sprintf("[%s]", floatSliceToCSV(vec))

	rows, err := db.Query(`
		SELECT product, remark, year, 1 - (embedding <-> $1::vector) AS score
		FROM remark_embeddings
		ORDER BY embedding <-> $1::vector
		LIMIT $2;
	`, vecLit, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.Product, &r.Remark, &r.Year, &r.Score); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, nil
}

// Helpers
func floatSliceToCSV(vec []float64) string {
	if len(vec) == 0 {
		return ""
	}
	out := fmt.Sprintf("%f", vec[0])
	for i := 1; i < len(vec); i++ {
		out += fmt.Sprintf(",%f", vec[i])
	}
	return out
}

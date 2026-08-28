package agentcore

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func readAll(r *http.Request, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, max))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return b, nil
}

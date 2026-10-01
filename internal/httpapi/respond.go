package httpapi

import (
	"net/http"

	"tpcall-backend/internal/httpx"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	httpx.WriteJSON(w, status, v)
}

package httpapi

import "net/http"

func NewRouter(handler *OrderHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", handler.Create)
	mux.HandleFunc("GET /orders/{id}", handler.Get)
	return mux
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

type Target struct {
	Id   int    `json:"id"`
	Name string `json:"name"`
	Addr string `json:"address"`
}

type TargetRequest struct {
	Name string `json:"name"`
	Addr string `json:"address"`
}

type Handler struct {
	store *MemoryStore
}

func httpInit(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
}

func healthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

func (h *Handler) listTarget(w http.ResponseWriter, r *http.Request) {
	httpInit(w)

	list := h.store.List()

	w.WriteHeader(http.StatusOK)
	err := json.NewEncoder(w).Encode(list)
	if err != nil {
		http.Error(w, "Server Error", http.StatusInternalServerError)
		return
	}
}

func (h *Handler) addTarget(w http.ResponseWriter, r *http.Request) {
	httpInit(w)
	var req TargetRequest

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "invaild json", http.StatusBadRequest)
		return
	}

	if req.Name == "" || req.Addr == "" {
		http.Error(w, "name and address are required", http.StatusBadRequest)
		return
	}

	target := h.store.Create(req)

	err2 := json.NewEncoder(w).Encode(target)
	if err2 != nil {
		http.Error(w, "Server Error", http.StatusInternalServerError)
		return
	}

}

func (h *Handler) getTarget(w http.ResponseWriter, r *http.Request) {
	httpInit(w)
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	target, err := h.store.Get(id)

	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(target)
}

func (h *Handler) updateTarget(w http.ResponseWriter, r *http.Request) {
	httpInit(w)
	var req TargetRequest

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	id, err := strconv.Atoi(r.PathValue("id"))

	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "invaild json", http.StatusBadRequest)
		return
	}
	target, err := h.store.Update(id, req)

	if err != nil {
		switch {
		case errors.Is(err, ErrTargetNotFound):
			http.Error(w, "invalid target", http.StatusNotFound)

		case errors.Is(err, ErrInvalidTarget):
			http.Error(w, "name and address are required", http.StatusBadRequest)

		default:
			http.Error(w, "server error", http.StatusInternalServerError)
		}
		return
	}

	err2 := json.NewEncoder(w).Encode(target)
	if err2 != nil {
		http.Error(w, "Server Error", http.StatusInternalServerError)
		return

	}

}

func (h *Handler) deleteTarget(w http.ResponseWriter, r *http.Request) {
	httpInit(w)
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	err2 := h.store.Delete(id)
	if err2 != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func NewRouter(store *MemoryStore) *http.ServeMux {
	mux := http.NewServeMux()
	h := &Handler{store: store}
	mux.HandleFunc("/healthz", healthz)

	mux.HandleFunc("GET /target", h.listTarget)
	mux.HandleFunc("POST /target", h.addTarget)

	mux.HandleFunc("GET /target/{id}", h.getTarget)
	mux.HandleFunc("PUT /target/{id}", h.updateTarget)
	mux.HandleFunc("DELETE /target/{id}", h.deleteTarget)
	return mux
}

func run() error {
	store := InitMemoryStore()
	mux := NewRouter(store)
	server := &http.Server{
		Addr:              ":9090",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,  // 客户端传入Header限制
		ReadTimeout:       8 * time.Second,  // 服务器读取整个请求时间限制
		WriteTimeout:      10 * time.Second, // 服务器写入响应时间限制
		IdleTimeout:       30 * time.Second, // HTTP keep-alive
	}

	serverErr := make(chan error, 1)

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	go func() {
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("listen and serve: %w", err)
		}
		log.Println("Server Normal exit")
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err := server.Shutdown(shutdownCtx)
		if err != nil {
			return fmt.Errorf("shutdown server: %w", err)
		}
		log.Println("get system stop signal,exit Http server")
		return nil
	}
}

func main() {
	err := run()
	if err != nil {
		log.Fatalf("something error : %+v", err)
	}
}

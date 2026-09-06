package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync"
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

var targets = make(map[int]Target)
var globalId = 0
var mu sync.Mutex

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

func listTarget(w http.ResponseWriter, r *http.Request) {
	httpInit(w)

	mu.Lock()
	list := make([]Target, 0, len(targets))
	for _, target := range targets {
		list = append(list, target)
	}
	mu.Unlock()

	w.WriteHeader(http.StatusOK)
	err := json.NewEncoder(w).Encode(list)
	if err != nil {
		http.Error(w, "Server Error", http.StatusInternalServerError)
		return
	}
}

func addTarget(w http.ResponseWriter, r *http.Request) {
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

	mu.Lock()
	nextId := globalId + 1
	globalId++

	target := Target{
		Id:   nextId,
		Name: req.Name,
		Addr: req.Addr,
	}

	targets[nextId] = target
	mu.Unlock()

	err2 := json.NewEncoder(w).Encode(target)
	if err2 != nil {
		http.Error(w, "Server Error", http.StatusInternalServerError)
		return
	}

}

func getTarget(w http.ResponseWriter, r *http.Request) {
	httpInit(w)
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	mu.Lock()
	target, ok := targets[id]
	mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(target)
}

func updateTarget(w http.ResponseWriter, r *http.Request) {
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
	mu.Lock()
	defer mu.Unlock()

	target, ok := targets[id]

	if !ok {
		http.Error(w, "invaild target", http.StatusNotFound)
		return
	}

	if req.Name == "" || req.Addr == "" {
		http.Error(w, "name and address are required", http.StatusBadRequest)
		return
	}

	target.Name = req.Name
	target.Addr = req.Addr

	targets[id] = target

	err2 := json.NewEncoder(w).Encode(target)
	if err2 != nil {
		http.Error(w, "Server Error", http.StatusInternalServerError)
		return

	}

}

func deleteTarget(w http.ResponseWriter, r *http.Request) {
	httpInit(w)
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	_, ok := targets[id]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	delete(targets, id)
	w.WriteHeader(http.StatusNoContent)
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)

	mux.HandleFunc("GET /target", listTarget)
	mux.HandleFunc("POST /target", addTarget)

	mux.HandleFunc("GET /target/{id}", getTarget)
	mux.HandleFunc("PUT /target/{id}", updateTarget)
	mux.HandleFunc("DELETE /target/{id}", deleteTarget)

	log.Println("Server start ok")
	err := http.ListenAndServe(":8080", mux)
	if err != nil {
		log.Fatal(err)
		return
	}
}

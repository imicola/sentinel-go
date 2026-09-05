package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func CheckCode(rec *httptest.ResponseRecorder, needCode int, t *testing.T) {
	t.Helper()
	if rec.Code != needCode {
		t.Errorf("stauts = %d,want %d,body = %s",
			rec.Code, needCode, rec.Body.String())
	}
}

func CheckHeader(rec *httptest.ResponseRecorder, needHeader string, t *testing.T) {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != needHeader {
		t.Errorf("Content-Type = %s,want %s", got, needHeader)
	}
}

func TestHealthz(t *testing.T) {

	router := http.NewServeMux()
	router.HandleFunc("/healthz", healthz)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	CheckCode(rec, http.StatusOK, t)
	CheckHeader(rec, "application/json", t)

	var body struct {
		Status string `json:"status"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body.Status != "ok" {
		t.Errorf("status field = %q, want ok", body.Status)
	}
}

func TestHealthzMethodNotAllowed(t *testing.T) {
	router := http.NewServeMux()
	router.HandleFunc("/healthz", healthz)

	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	CheckCode(rec, http.StatusMethodNotAllowed, t)
}

func TestCreateAndGetTarget(t *testing.T) {
	mu.Lock()
	targets = make(map[int]Target)
	globalId = 0
	mu.Unlock()

	router := http.NewServeMux()
	router.HandleFunc("POST /target", addTarget)
	router.HandleFunc("GET /target/{id}", getTarget)
	router.HandleFunc("PUT /target/{id}", updateTarget)
	router.HandleFunc("DELETE /target/{id}", deleteTarget)

	// == create ==

	createReq := httptest.NewRequest(
		http.MethodPost,
		"/target",
		strings.NewReader(`{"name":"demo","address":"localhost:8080"}`),
	)
	createReq.Header.Set("Content-Type", "application/json")

	createRec := httptest.NewRecorder()

	router.ServeHTTP(createRec, createReq)

	CheckCode(createRec, http.StatusOK, t)
	CheckHeader(createRec, "application/json", t)

	checkTarget := func(rec *httptest.ResponseRecorder) (got Target) {
		t.Helper()
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return
	}

	created := checkTarget(createRec)

	if created.Id <= 0 {
		t.Fatal("Construction error,id should not less than 0")
	}

	if created.Name != "demo" || created.Addr != "localhost:8080" {
		t.Errorf("wrong result,body = %s", createRec.Body.String())
	}

	// == GET : 1 ==

	path := "/target/" + strconv.Itoa(created.Id)

	getReq := httptest.NewRequest(
		http.MethodGet,
		path,
		nil,
	)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)

	CheckCode(getRec, http.StatusOK, t)
	CheckHeader(getRec, "application/json", t)
	geted := checkTarget(getRec)

	if geted.Id != created.Id {
		t.Fatalf("GET id = %d, want %d", geted.Id, created.Id)
	}

	if geted.Name != "demo" || geted.Addr != "localhost:8080" {
		t.Errorf("wrong result,body = %s", getRec.Body.String())
	}

	// == PUT ==

	putReq := httptest.NewRequest(
		http.MethodPut,
		path,
		strings.NewReader(`{"name":"updated","address":"localhost:9090"}`),
	)
	putRec := httptest.NewRecorder()

	router.ServeHTTP(putRec, putReq)

	CheckCode(putRec, http.StatusOK, t)
	CheckHeader(putRec, "application/json", t)

	putres := checkTarget(putRec)

	if putres.Id != created.Id {
		t.Fatalf("GET id = %d, want %d", putres.Id, created.Id)
	}

	if putres.Name != "updated" || putres.Addr != "localhost:9090" {
		t.Errorf("wrong result,body = %s", putRec.Body.String())
	}

	// == GET : 2 ==

	reGetReq := httptest.NewRequest(
		http.MethodGet,
		path,
		nil,
	)
	reGetRec := httptest.NewRecorder()

	router.ServeHTTP(reGetRec, reGetReq)

	CheckCode(reGetRec, http.StatusOK, t)
	CheckHeader(reGetRec, "application/json", t)
	reGet := checkTarget(reGetRec)

	if reGet.Id != created.Id {
		t.Fatalf("GET id = %d, want %d", reGet.Id, created.Id)
	}

	if reGet.Name != "updated" || reGet.Addr != "localhost:9090" {
		t.Errorf("wrong result,body = %s", reGetRec.Body.String())
	}

	// == DELETE ==
	deleteReq := httptest.NewRequest(
		http.MethodDelete,
		path,
		nil,
	)
	deleteRec := httptest.NewRecorder()

	router.ServeHTTP(deleteRec, deleteReq)
	CheckCode(deleteRec, http.StatusNoContent, t)

	if deleteRec.Body.Len() != 0 {
		t.Errorf("DELETE body = %q, want empty", deleteRec.Body.String())
	}

	// == GET : 3 ==

	rreGetReq := httptest.NewRequest(
		http.MethodGet,
		path,
		nil,
	)
	rreGetRec := httptest.NewRecorder()

	router.ServeHTTP(rreGetRec, rreGetReq)

	CheckCode(rreGetRec, http.StatusNotFound, t)
}

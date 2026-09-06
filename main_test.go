package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
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

var testHandler string = "application/json"

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

	// == POST ==

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

func TestPostWithErrorJson(t *testing.T) {

	mu.Lock()
	targets = make(map[int]Target)
	globalId = 0
	mu.Unlock()

	router := http.NewServeMux()
	router.HandleFunc("POST /target", addTarget)

	postRec := httptest.NewRecorder()

	body := `{"name":"Error","address":`

	postReq := httptest.NewRequest(
		http.MethodPost,
		"/target",
		strings.NewReader(body),
	)

	// Act
	router.ServeHTTP(postRec, postReq)

	// Assert: response
	CheckCode(postRec, 400, t)
	CheckHeader(postRec, "text/plain; charset=utf-8", t)

	// Assert: Status
	mu.Lock()
	targetlen := len(targets)
	id := globalId
	mu.Unlock()

	if targetlen != 0 {
		t.Fatalf("invalid POST changed targets: got %d targets, want 0", targetlen)
	}

	if id != 0 {
		t.Fatalf("invalid POST changed globalId: got %d, want 0", id)
	}
}

func TestPostMissingField(t *testing.T) {
	mu.Lock()
	targets = make(map[int]Target)
	globalId = 0
	mu.Unlock()

	router := http.NewServeMux()
	router.HandleFunc("POST /target", addTarget)

	postRec := httptest.NewRecorder()

	body := `{"name":"missAddress"}`

	postReq := httptest.NewRequest(
		http.MethodPost,
		"/target",
		strings.NewReader(body),
	)

	// Act
	router.ServeHTTP(postRec, postReq)

	// Assert : HTTP Response

	CheckCode(postRec, 400, t)
	CheckHeader(postRec, "text/plain; charset=utf-8", t)

	// Assert : Ststus

	mu.Lock()
	targetlen := len(targets)
	id := globalId
	mu.Unlock()

	if targetlen != 0 {
		t.Fatalf("invalid POST changed targets: got %d targets, want 0", targetlen)
	}

	if id != 0 {
		t.Fatalf("invalid POST changed globalId: got %d, want 0", id)
	}
}

// 空列表、创建两条后的列表
func TestListCheck(t *testing.T) {
	/*
		request：
		1.GET 	=> return empty list
		2.POST 	=> tow targe add
		3.GET 	=> return tow targe
	*/

	mu.Lock()
	targets = make(map[int]Target)
	globalId = 0
	mu.Unlock()

	router := http.NewServeMux()
	router.HandleFunc("GET /target", listTarget)
	router.HandleFunc("POST /target", addTarget)

	getRec := httptest.NewRecorder()
	getReq := httptest.NewRequest(
		http.MethodGet,
		"/target",
		nil,
	)

	checkTarget := func(rec *httptest.ResponseRecorder) (got Target) {
		t.Helper()
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return
	}

	// ACT GET
	router.ServeHTTP(getRec, getReq)

	// Assert GET

	CheckCode(getRec, 200, t)
	CheckHeader(getRec, testHandler, t)

	mu.Lock()
	targetslen := len(targets)
	id := globalId
	mu.Unlock()

	if targetslen != 0 {
		t.Fatalf("invalid GET changed targets: got %d targets, want 0", targetslen)
	}

	if id != 0 {
		t.Fatalf("invalid GET changed globalId: got %d, want 0", id)
	}

	// Assert Status
	var emptyList []Target
	if err := json.Unmarshal(getRec.Body.Bytes(), &emptyList); err != nil {
		t.Fatalf("decode empty list: %v", err)
	}
	if len(emptyList) != 0 {
		t.Fatalf("list length = %d, want 0", len(emptyList))
	}

	// POST

	testTarget := []struct {
		Name string `json:"name"`
		Addr string `json:"address"`
	}{
		{
			"test1",
			"localhost:1010",
		},
		{
			"test2",
			"localhost:2020",
		},
	}

	wantm := make(map[int]Target)

	for i := 0; i < 2; i++ {
		body, err := json.Marshal(testTarget[i])

		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}

		postRec := httptest.NewRecorder()
		postReq := httptest.NewRequest(
			http.MethodPost,
			"/target",
			bytes.NewReader(body),
		)

		// Act
		router.ServeHTTP(postRec, postReq)

		// Assert : Response
		CheckCode(postRec, 200, t)
		CheckHeader(postRec, testHandler, t)

		// Assert : check Status

		gotTarget := checkTarget(postRec)
		needTarget := testTarget[i]

		if gotTarget.Name != needTarget.Name || gotTarget.Addr != needTarget.Addr {
			t.Errorf("wrong Target object,body = %+v,need %+v", gotTarget, needTarget)
		}

		wantm[gotTarget.Id] = gotTarget
	}

	if len(wantm) != 2 {
		t.Errorf("Incorrect Target quantity %d,need %d", len(wantm), 2)
	}

	// GET 2

	get2Rec := httptest.NewRecorder()
	get2Req := httptest.NewRequest(
		http.MethodGet,
		"/target",
		nil,
	)

	// ACT GET
	router.ServeHTTP(get2Rec, get2Req)

	// Assert GET

	CheckCode(get2Rec, 200, t)
	CheckHeader(get2Rec, testHandler, t)

	// Assert Status

	var got []Target
	if err := json.Unmarshal(get2Rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	gotm := make(map[int]Target)

	for _, v := range got {
		gotm[v.Id] = v
	}

	if len(gotm) != len(wantm) {
		t.Fatalf(
			"wrong target count: got %d, want %d",
			len(gotm),
			len(wantm),
		)
	}

	for id, want := range wantm {
		gotTarget, ok := gotm[id]

		if !ok {
			t.Errorf("target id %d not found", id)
			continue
		}

		if gotTarget != want {
			t.Errorf(
				"id %d: got %+v, want %+v",
				id,
				gotTarget,
				want,
			)
		}
	}
}

func TestConcurrentCreateTarget(t *testing.T) {
	// 1. 重置 targets 和 globalId

	mu.Lock()
	targets = make(map[int]Target)
	globalId = 0
	mu.Unlock()
	// 2. 创建 router，注册 POST /target

	router := http.NewServeMux()
	router.HandleFunc("POST /target", addTarget)

	const count = 50

	type result struct {
		code         int
		target       Target
		marshalErr   error
		unmarshalErr error
	}

	results := make(chan result, count)

	var wg sync.WaitGroup
	wg.Add(count)
	start := make(chan struct{})
	for i := 0; i < count; i++ {
		go func(i int) {
			defer wg.Done()

			// 每个 goroutine：
			// 1. 生成独立的 name 和 address
			target := struct {
				Name    string `json:"name"`
				Address string `json:"address"`
			}{

				"test" + strconv.Itoa(i),
				"localhost:" + strconv.Itoa(100*(i+1)),
			}

			body, err := json.Marshal(target)
			// 2. 创建独立的 Request 和 Recorder
			postRec := httptest.NewRecorder()
			postReq := httptest.NewRequest(
				http.MethodPost,
				"/target",
				bytes.NewReader(body),
			)
			// 3. 调用 router.ServeHTTP
			<-start
			router.ServeHTTP(postRec, postReq)
			// 4. 解析返回的 Target
			var res Target
			parseErr := json.Unmarshal(postRec.Body.Bytes(), &res)
			// 5. 把 code、Target、解析错误发进 results
			results <- result{
				code:         postRec.Code,
				target:       res,
				marshalErr:   err,
				unmarshalErr: parseErr,
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	// 统一遍历 results：
	// 1. 每个状态码必须是 200
	// 2. 每个 JSON 必须能解析
	// 3. ID 必须大于 0
	// 4. ID 不能重复
	// 5. 最终必须收集到 count 个不同 ID
	rep := make(map[int]int, count)
	tot := 0
	for res := range results {
		if res.code != http.StatusOK {
			t.Errorf("stauts = %d,want %d", res.code, 200)
		}
		if res.marshalErr != nil {
			t.Errorf("marshal request body: %v", res.marshalErr)
		}
		if res.unmarshalErr != nil {
			t.Errorf("unmarshal request body: %v", res.unmarshalErr)
		}
		target := res.target
		if target.Id <= 0 {
			t.Errorf("Id field error,id = %d,should greater than 0", target.Id)
		}
		_, ok := rep[target.Id]
		if ok {
			t.Errorf("Id = %d repetition", target.Id)
		} else {
			tot++
			rep[target.Id] = 1
		}
	}
	if tot != count {
		t.Errorf("Id insufficient,Missing some target")
	}

	mu.Lock()
	targetCount := len(targets)
	lastID := globalId
	mu.Unlock()

	if targetCount != count {
		t.Errorf("stored targets = %d, want %d", targetCount, count)
	}
	if lastID != count {
		t.Errorf("globalId = %d, want %d", lastID, count)
	}
}

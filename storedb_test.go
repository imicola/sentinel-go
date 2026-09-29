package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func registerStore(t *testing.T) *PostgresStore {
	t.Helper()
	if os.Getenv("RUN_POSTGRES_TESTS") != "1" {
		t.Skip("set RUN_POSTGRES_TESTS=1 to run PostgreSQL integration tests")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Fatal("DATABASE_URL is required when PostgreSQL tests are enabled")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("Open Error: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	store := NewPostgresStore(db)
	ctxWithTime, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := store.db.PingContext(ctxWithTime); err != nil {
		t.Fatalf("Connect Error : %v", err)
	}
	return store
}

func TestPostgresStoreCreate(t *testing.T) {
	store := registerStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	targetReq := TargetRequest{
		Name: "testing",
		Addr: "127.0.0.1:1010",
	}
	targetRec, err := store.Create(ctx, targetReq)
	if err != nil {
		t.Fatalf("Create Error: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		_, err := store.db.ExecContext(
			cleanupCtx,
			"DELETE FROM targets WHERE id = $1",
			targetRec.Id,
		)
		if err != nil {
			t.Errorf("cleanup created target: %v", err)
		}
	})
	if targetRec.Id <= 0 {
		t.Errorf("Create Id error: %d", targetRec.Id)
	}
	if targetRec.Name != targetReq.Name {
		t.Errorf("Created Return Name Error:%s", targetRec.Name)
	}
	if targetRec.Addr != targetReq.Addr {
		t.Errorf("Created Return Addr Error:%s", targetRec.Addr)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	if err := store.db.PingContext(ctx2); err != nil {
		t.Fatalf("Connect unexpected close : %v", err)
	}

}

func TestPostGresStoreGet(t *testing.T) {
	storeA := registerStore(t)
	storeB := NewPostgresStore(storeA.db)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	targetAReq := TargetRequest{
		Name: "test",
		Addr: "127.0.0.1:1010",
	}
	targetARec, errA := storeA.Create(ctx, targetAReq)
	if errA != nil {
		t.Fatalf("Create Error: %v", errA)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		_, err := storeA.db.ExecContext(
			cleanupCtx,
			"DELETE FROM targets WHERE id = $1",
			targetARec.Id,
		)
		if err != nil {
			t.Errorf("cleanup created target: %v", err)
		}
	})
	targetBReq := targetARec.Id
	targetBRec, errB := storeB.Get(ctx, targetBReq)
	if errB != nil {
		if errors.Is(errB, ErrTargetNotFound) {
			t.Errorf("Target Not Found")
		} else {
			t.Errorf("Get Error: %v", errB)
		}
	}

	if targetARec != targetBRec {
		t.Errorf("Target Incoming and outgoing different,targetA: %v ,targetB: %v", targetARec, targetBRec)
	}
}

func TestPGSInvalidGet(t *testing.T) {
	store := registerStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	targetReq := -1
	targetRec, err := store.Get(ctx, targetReq)
	if err == nil {
		t.Errorf("Unreasonable return,Error not exist")
	}
	if !errors.Is(err, ErrTargetNotFound) {
		t.Errorf("Unreasonable return,Error = %v,should be %v", err, ErrTargetNotFound)
	}
	if targetRec != (Target{}) {
		t.Errorf("Target wrong write，should be empty Target")
	}

}

func TestPostgresStoreGetCanceledContext(t *testing.T) {
	store := registerStore(t)

	ctxWithCancel, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	cancel2()
	targetRes, err := store.Get(ctxWithCancel, -1)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Cancel fail，err = %v", err)
	}
	if targetRes != (Target{}) {
		t.Errorf("Target wrong write，should be empty Target")
	}
	ctxWithTime, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := store.db.PingContext(ctxWithTime); err != nil {
		t.Fatalf("Connect Error : %v", err)
	}
}

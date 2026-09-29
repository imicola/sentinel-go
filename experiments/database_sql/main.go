package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		log.Fatal(err)
	}

	var result int

	// TODO：使用 QueryRowContext 执行：
	// SELECT $1::int + $2::int
	// 参数是 20 和 22，并把返回值 Scan 到 result。
	err = db.QueryRowContext(
		ctx,
		"SELECT $1::int + $2::int",
		20, 22,
	).Scan(&result)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result)
}

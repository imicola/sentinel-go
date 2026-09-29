package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (store *PostgresStore) Create(
	ctx context.Context,
	req TargetRequest,
) (Target, error) {

	var target Target

	query := `INSERT INTO targets (name, address)
			  VALUES ($1, $2)
			  RETURNING id, name, address`

	// 1. 执行上面的 INSERT ... RETURNING。
	row := store.db.QueryRowContext(ctx, query, req.Name, req.Addr)
	// 2. 把返回的 id、name、address Scan 到 target。
	err := row.Scan(&target.Id, &target.Name, &target.Addr)
	// 3. 如果 Scan 出错，返回空 Target 和错误。
	if err != nil {
		return Target{}, fmt.Errorf("create target: %w", err)
	}
	// 4. 成功时返回 target 和 nil。
	return target, nil
}

func (store *PostgresStore) Get(
	ctx context.Context,
	id int,
) (Target, error) {

	var target Target

	query := `SELECT id, name, address
			  FROM targets
			WHERE id = $1`

	row := store.db.QueryRowContext(ctx, query, id)
	// 2. 把返回的 id、name、address Scan 到 target。
	err := row.Scan(&target.Id, &target.Name, &target.Addr)
	if errors.Is(err, sql.ErrNoRows) {
		return Target{}, ErrTargetNotFound
	}
	if err != nil {
		return Target{}, fmt.Errorf("Get operation err: %w", err)
	}
	return target, nil
}

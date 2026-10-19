package service

import (
	"context"
	"database/sql"
	"encoding/json"
)

type Job struct {
	ID        string          `json:"id"`
	State     string          `json:"state"`
	Data      json.RawMessage `json:"data"`
	CreatedAt string          `json:"created_at"`
}

func (s *Store) Jobs(ctx context.Context, account, cursor string, limit int) (Page, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,state,data,created_at FROM jobs WHERE account_id=? AND id>? ORDER BY id LIMIT ?", account, cursor, limit+1)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	items := []Job{}
	for rows.Next() {
		var j Job
		if err = rows.Scan(&j.ID, &j.State, &j.Data, &j.CreatedAt); err != nil {
			return Page{}, err
		}
		items = append(items, j)
	}
	p := Page{Items: items}
	if len(items) > limit {
		p.Items = items[:limit]
		p.NextCursor = items[limit-1].ID
	}
	return p, rows.Err()
}
func (s *Store) Job(ctx context.Context, account, id string, cancel bool) (Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	var j Job
	err = tx.QueryRowContext(ctx, "SELECT id,state,data,created_at FROM jobs WHERE account_id=? AND id=?", account, id).Scan(&j.ID, &j.State, &j.Data, &j.CreatedAt)
	if err == sql.ErrNoRows {
		return j, fail("not_found", "job not found for this account", 404)
	}
	if err != nil {
		return j, err
	}
	if cancel {
		if j.State != "queued" {
			return j, fail("conflict", "only queued jobs may be cancelled", 409)
		}
		if _, err = tx.ExecContext(ctx, "UPDATE jobs SET state='cancelled' WHERE account_id=? AND id=?", account, id); err != nil {
			return j, err
		}
		j.State = "cancelled"
		if err = appendEvent(ctx, tx, account, "job.cancelled", map[string]string{"job_id": id}); err != nil {
			return j, err
		}
	}
	return j, tx.Commit()
}

package main

import (
	"context"
	"encoding/json"
	"io"
)

// SnapshotReader负责观察；RunPreflight只连接观察、纯判断与输出。
type SnapshotReader interface {
	ReadSnapshot(context.Context) Snapshot
}

func RunPreflight(ctx context.Context, reader SnapshotReader, out io.Writer) (int, error) {
	report := Analyze(reader.ReadSnapshot(ctx))
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return 1, err
	}
	switch report.Verdict {
	case VerdictObservedReady:
		return 0, nil
	case VerdictNeedsReview:
		return 3, nil
	case VerdictBlocked:
		return 4, nil
	default:
		return 5, nil
	}
}

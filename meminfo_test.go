package main

import (
	"errors"
	"strings"
	"testing"
)

func TestParseMemAvailable(t *testing.T) {
	// 正常样本：MemAvailable: 2048 kB → 2097152 bytes
	r := `MemTotal:        8192000 kB
MemFree:         1024000 kB
MemAvailable:    3072000 kB
Buffers:          128000 kB`
	res, err := parseMemAvailable(strings.NewReader(r))
	if err != nil {
		t.Errorf("Obtained an error that should not exist，err = %v", err)
	}
	if res != 3145728000 {
		t.Errorf("numerical value error: %d,need 3145728000", res)
	}
	// 缺少 MemAvailable 行 → 错误
	rErr1 := `MemTotal:        8192000 kB
MemFree:         1024000 kB
Buffers:          128000 kB`
	res2, err := parseMemAvailable(strings.NewReader(rErr1))
	if !errors.Is(err, ErrMemAvailableMissing) {
		t.Errorf("expected ErrMemAvailableMissing,got %v", err)
	}
	if res2 != 0 {
		t.Errorf("numerical value error: %d,need 0", res2)
	}
	// 数字不是整数 → 错误
	rErr2 := `MemTotal:        8192000 kB
MemFree:         1024000 kB
MemAvailable:    3072000.3746 kB
Buffers:          128000 kB`
	res3, err := parseMemAvailable(strings.NewReader(rErr2))
	if !errors.Is(err, ErrMemAvailableInvalid) {
		t.Errorf("expected ErrMemAvailableInvalid,got %v", err)
	}
	if res3 != 0 {
		t.Errorf("numerical value error: %d,need 0", res3)
	}
	// 单位不是 kB → 错误
	rErr3 := `MemTotal:        8192000 mB
MemFree:         1024000 mB
MemAvailable:    3072000 mB
Buffers:          128000 mB`
	res4, err := parseMemAvailable(strings.NewReader(rErr3))
	if !errors.Is(err, ErrMemAvailableInvalid) {
		t.Errorf("expected ErrMemAvailableInvalid,got %v", err)
	}
	if res4 != 0 {
		t.Errorf("numerical value error: %d,need 0", res4)
	}
	// 数字过大溢出
	rErr4 := `MemTotal:        8192000 kB
MemFree:         1024000 kB
MemAvailable:    18014398509481984 kB
Buffers:          128000 kB`
	res5, err := parseMemAvailable(strings.NewReader(rErr4))
	if !errors.Is(err, ErrMemAvailableNumInvalid) {
		t.Errorf("expected ErrMemAvailableNumInvalid,got %v", err)
	}
	if res5 != 0 {
		t.Errorf("numerical value error: %d,need 0", res5)
	}

}

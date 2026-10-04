package main

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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

func TestReadMemAvailable(t *testing.T) {
	// 正常临时文件
	dir := t.TempDir()
	path := filepath.Join(dir, "meminfo")
	content := []byte(
		`MemTotal:        8192000 kB
MemFree:         1024000 kB
MemAvailable:    3072000 kB
Buffers:          128000 kB`)
	err := os.WriteFile(path, content, 0644)
	if err != nil {
		t.Fatalf("Write Error %v", err)
	}
	res, err := readMemAvailable(path)
	if err != nil {
		t.Errorf("Get Error : %v", err)
	}
	if res != 3145728000 {
		t.Errorf("numerical value error: %d,need 3145728000", res)
	}
	// 内容格式错误
	contentFormatErr := []byte(
		`MemTotal:        8192000 kb
MemFree:         1024000 kb
MemAvailable:    3072000 kb
Buffers:          128000 kb`)
	err2 := os.WriteFile(path, contentFormatErr, 0644)
	if err2 != nil {
		t.Fatalf("Write Error %v", err2)
	}
	res2, err2 := readMemAvailable(path)
	if !errors.Is(err2, ErrMemAvailableInvalid) {
		t.Errorf("expected ErrMemAvailableInvalid,got %v", err2)
	}
	if res2 != 0 {
		t.Errorf("return num need 0,got %v", res2)
	}
	// 路径不存在
	path2 := filepath.Join(dir, "missing")
	res3, err3 := readMemAvailable(path2)
	if !errors.Is(err3, fs.ErrNotExist) {
		t.Errorf("expected fs.ErrNotExist,got %v", err3)
	}
	if res3 != 0 {
		t.Errorf("return num need 0,got %v", res3)
	}
}

func TestGetMemAvailable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("require Linux /proc/meminfo")
	}
	got, err := readMemAvailable("/proc/meminfo")
	if err != nil {
		t.Fatalf("Get Error : %v", err)
	}
	t.Logf("MemAvailable = %d bytes", got)
}

func TestCopyPathMemAvailable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("require Linux /proc/meminfo")
	}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		t.Fatalf("Open Error : %v", err)
	}
	defer f.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "meminfo")
	dst, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create Error : %v", err)
	}
	defer dst.Close()
	_, err = io.Copy(dst, f)
	if err != nil {
		t.Fatalf("Copy Error : %v", err)
	}
	read, err := readMemAvailable(path)
	if err != nil {
		t.Fatalf("Read Error : %v", err)
	}
	_, err = dst.Seek(0, io.SeekStart)
	if err != nil {
		t.Fatalf("Seek Error : %v", err)
	}
	real, err := parseMemAvailable(bufio.NewReader(dst))
	if err != nil {
		t.Fatalf("Read Error : %v", err)
	}
	if read != real {
		t.Errorf("read value %d not equal to real value %d", read, real)
	}
	_, err = dst.Seek(0, io.SeekStart)
	if err != nil {
		t.Fatalf("Seek Error : %v", err)
	}
	scanner := bufio.NewScanner(dst)
	var raw uint64
	for scanner.Scan() {
		field := strings.Fields(scanner.Text())
		if len(field) == 0 || field[0] != "MemAvailable:" {
			continue
		}
		tmp, err := strconv.ParseUint(field[1], 10, 64)
		raw = tmp
		if err != nil {
			t.Fatalf("Atoi Error : %v", err)
		}
	}
	if scanner.Err() != nil {
		t.Fatalf("Scan Error : %v", scanner.Err())
	}
	if raw*1024 != read {
		t.Errorf("raw value %d not equal to read value %d", raw*1024, read)
	}
}

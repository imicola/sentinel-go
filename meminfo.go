// meminfo.go
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

var (
	ErrMemAvailableMissing    = errors.New("MemAvailable field missing")
	ErrMemAvailableInvalid    = errors.New("MemAvailable field invalid")
	ErrMemAvailableNumInvalid = errors.New("MemAvailable number invalid")
)

/*	/proc/meminfo 格式
 *
 * 	MemTotal:        8192000 kB
 *	MemFree:         1024000 kB
 *	MemAvailable:    3072000 kB
 *	Buffers:          128000 kB
 */

func parseMemAvailable(r io.Reader) (uint64, error) {
	// 1. 逐行读取 r
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		field := strings.Fields(scanner.Text())
		// 2. 找到以 MemAvailable: 开头的那一行
		if len(field) == 0 || field[0] != "MemAvailable:" {
			continue
		}
		if len(field) != 3 || field[2] != "kB" {
			return 0, fmt.Errorf("%w: invalid MemAvailable line", ErrMemAvailableInvalid)
		}
		// 3. 拆出数字和单位，检查格式
		kb, err := strconv.ParseUint(field[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: invalid MemAvailable value", ErrMemAvailableInvalid)
		}
		// 4. 将数字从 kB 换算成 bytes 后返回
		if kb > math.MaxUint64/1024 {
			return 0, ErrMemAvailableNumInvalid
		}
		return kb * 1024, nil
	}
	// 5. 读完仍没找到，或格式不对，就返回错误
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, ErrMemAvailableMissing
}

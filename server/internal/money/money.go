// Package money 定义 micro-USDT 整数金额：1 USDT = 1_000_000 micro（方案 6.2）。
// 接口层金额一律字符串，最多 6 位小数，超精度直接拒绝而非四舍五入。
package money

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

type Micro int64

const (
	OneUSDT   Micro = 1_000_000
	TenthUSDT Micro = 100_000 // 基础金额的最小步进：0.1 USDT
	TailStep  Micro = 10_000  // 唯一尾数步进：0.01 USDT
	TailSlots       = 99      // 尾数取值 1..99，即 0.01..0.99
)

var ErrFormat = errors.New("金额格式错误或超出精度")

// Parse 解析形如 "100.0043" 的字符串。允许可选前导 0、最多 6 位小数，不允许负数、指数、千分位。
func Parse(s string) (Micro, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
		return 0, ErrFormat
	}
	intPart, fracPart, _ := strings.Cut(s, ".")
	if intPart == "" || len(fracPart) > 6 || !digits(intPart) || (fracPart != "" && !digits(fracPart)) {
		return 0, ErrFormat
	}
	if len(intPart) > 12 { // 上限 1e12 USDT，远超业务范围
		return 0, ErrFormat
	}
	ip, _ := strconv.ParseInt(intPart, 10, 64)
	fp := int64(0)
	if fracPart != "" {
		fp, _ = strconv.ParseInt(fracPart+strings.Repeat("0", 6-len(fracPart)), 10, 64)
	}
	return Micro(ip*int64(OneUSDT) + fp), nil
}

// String 固定 6 位小数，如 100004300 → "100.004300"，与接口示例一致。
func (m Micro) String() string {
	neg := m < 0
	magnitude := uint64(m)
	if neg {
		// 避免最小 int64 取负溢出；账单读取需要正确展示完整 BIGINT 范围。
		magnitude = uint64(-(m + 1)) + 1
	}
	s := fmt.Sprintf("%d.%06d", magnitude/uint64(OneUSDT), magnitude%uint64(OneUSDT))
	if neg {
		return "-" + s
	}
	return s
}

// Display 去掉尾随零但至少保留 2 位小数，供支付页突出“到账数量必须等于 6.01”。
func (m Micro) Display() string {
	s := m.String()
	for strings.HasSuffix(s, "0") && len(s)-strings.IndexByte(s, '.') > 3 {
		s = s[:len(s)-1]
	}
	return s
}

// CeilToTenth 向上取整到 0.1 USDT（报价基础金额，方案 5.2）。
func CeilToTenth(m Micro) Micro {
	if m <= 0 {
		return 0
	}
	return (m + TenthUSDT - 1) / TenthUSDT * TenthUSDT
}

// RandomTail 生成 0.01~0.99 USDT 的唯一尾数（安全随机，非递增，避免推测订单量）。
func RandomTail() (Micro, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(TailSlots))
	if err != nil {
		return 0, err
	}
	return Micro(n.Int64()+1) * TailStep, nil
}

// SplitTail 从支付金额中拆出整数 USDT 与 0.01 步进的尾数。
func SplitTail(pay Micro) (base, tail Micro) {
	tail = pay % OneUSDT
	if tail < 0 {
		tail += OneUSDT
	}
	return pay - tail, tail
}

func digits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

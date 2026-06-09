package utils

import (
	"crypto/rand"
	"math/big"
)

func GenerateRandomDigits(n int) string {
	if n <= 0 {
		return ""
	}

	const digits = "0123456789"
	ret := make([]byte, n)
	for i := 0; i < n; i++ {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		if err != nil {
			// In the incredibly rare case crypto/rand fails, we fallback or panic.
			// For an OTP system, panicking is safer than returning weak randomness.
			panic(err)
		}
		ret[i] = digits[num.Int64()]
	}

	return string(ret)
}

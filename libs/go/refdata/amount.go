package refdata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

// Amount is an exact signed integer in atomic units.
//
// The zero value is zero. JSON is always an integer literal, never a string and
// never a floating-point value, so Rust's i128 serde representation is a strict
// superset. Arithmetic is checked by callers against MaxAtomicAmount.
type Amount struct {
	value *big.Int
}

// MaxAtomicExponent is the largest supported amount: 10^30 atomic units.
const MaxAtomicExponent = 30

// NewAmount builds an amount from a signed 64-bit integer.
func NewAmount(value int64) Amount {
	return Amount{value: big.NewInt(value)}
}

// ParseAmount parses a canonical integer literal.
func ParseAmount(input string) (Amount, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return Amount{}, fmt.Errorf("amount must not be empty")
	}
	value, ok := new(big.Int).SetString(input, 10)
	if !ok {
		return Amount{}, fmt.Errorf("amount must be an integer")
	}
	if strings.ContainsAny(input, ".eE+") {
		return Amount{}, fmt.Errorf("amount must be an integer")
	}
	return Amount{value: value}, nil
}

// MustAmount parses a trusted test or fixture literal.
func MustAmount(input string) Amount {
	value, err := ParseAmount(input)
	if err != nil {
		panic(err)
	}
	return value
}

// MaxAtomicAmount returns 10^30.
func MaxAtomicAmount() Amount {
	return MustAmount("1" + strings.Repeat("0", MaxAtomicExponent))
}

// String renders the canonical decimal integer.
func (a Amount) String() string {
	if a.value == nil {
		return "0"
	}
	return a.value.String()
}

// Big returns a copy of the underlying integer.
func (a Amount) Big() *big.Int {
	if a.value == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(a.value)
}

// IsZero reports whether the amount is zero.
func (a Amount) IsZero() bool { return a.Sign() == 0 }

// IsNegative reports whether the amount is negative.
func (a Amount) IsNegative() bool { return a.Sign() < 0 }

// IsPositive reports whether the amount is positive.
func (a Amount) IsPositive() bool { return a.Sign() > 0 }

// Sign returns -1, 0, or +1.
func (a Amount) Sign() int {
	if a.value == nil {
		return 0
	}
	return a.value.Sign()
}

// Cmp compares two amounts.
func (a Amount) Cmp(other Amount) int {
	if a.value == nil {
		if other.value == nil {
			return 0
		}
		return new(big.Int).Neg(other.value).Sign()
	}
	if other.value == nil {
		return a.value.Sign()
	}
	return a.value.Cmp(other.value)
}

// Equal reports exact equality.
func (a Amount) Equal(other Amount) bool { return a.Cmp(other) == 0 }

// Add returns a + other.
func (a Amount) Add(other Amount) Amount {
	return Amount{value: new(big.Int).Add(a.Big(), other.Big())}
}

// Sub returns a - other.
func (a Amount) Sub(other Amount) Amount {
	return Amount{value: new(big.Int).Sub(a.Big(), other.Big())}
}

// Neg returns -a.
func (a Amount) Neg() Amount {
	return Amount{value: new(big.Int).Neg(a.Big())}
}

// Mul returns a * factor.
func (a Amount) Mul(factor int64) Amount {
	return Amount{value: new(big.Int).Mul(a.Big(), big.NewInt(factor))}
}

// QuoRem divides by a positive divisor, returning the truncated quotient and the
// non-negative remainder as int64. Divisors come from validated bps ranges.
func (a Amount) QuoRem(divisor int64) (Amount, int64, error) {
	if divisor <= 0 {
		return Amount{}, 0, fmt.Errorf("divisor must be positive")
	}
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(a.Big(), big.NewInt(divisor), remainder)
	return Amount{value: quotient}, remainder.Int64(), nil
}

// Exceeds reports whether the absolute value is greater than limit.
func (a Amount) Exceeds(limit Amount) bool {
	return new(big.Int).Abs(a.Big()).Cmp(limit.Big()) > 0
}

// MarshalJSON emits a JSON integer literal.
func (a Amount) MarshalJSON() ([]byte, error) {
	return []byte(a.String()), nil
}

// UnmarshalJSON accepts only a JSON integer literal.
func (a *Amount) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		a.value = nil
		return nil
	}
	if raw[0] == '"' {
		return fmt.Errorf("amount must be a JSON integer, not a string")
	}
	if bytes.ContainsAny(raw, ".eE+") {
		return fmt.Errorf("amount must be a JSON integer without fraction or exponent")
	}
	value, ok := new(big.Int).SetString(string(raw), 10)
	if !ok {
		return fmt.Errorf("amount must be a JSON integer")
	}
	a.value = value
	return nil
}

// MarshalText emits the canonical decimal string.
func (a Amount) MarshalText() ([]byte, error) { return []byte(a.String()), nil }

// UnmarshalText parses a canonical decimal string.
func (a *Amount) UnmarshalText(raw []byte) error {
	value, err := ParseAmount(string(raw))
	if err != nil {
		return err
	}
	*a = value
	return nil
}

// Ensure Amount implements json.Marshaler/Unmarshaler.
var (
	_ json.Marshaler   = Amount{}
	_ json.Unmarshaler = (*Amount)(nil)
)

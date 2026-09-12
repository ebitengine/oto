// Copyright 2026 The Oto Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mathutil_test

import (
	"math"
	"math/big"
	"testing"

	"github.com/ebitengine/oto/v3/internal/mathutil"
)

func TestMulDiv(t *testing.T) {
	values := []int64{
		math.MinInt64,
		math.MinInt64 + 1,
		-1_000_000_000,
		-10_000_000,
		-48000,
		-3,
		-2,
		-1,
		0,
		1,
		2,
		3,
		48000,
		10_000_000,
		1_000_000_000,
		math.MaxInt64 - 1,
		math.MaxInt64,
	}
	for _, x := range values {
		for _, mul := range values {
			for _, div := range values {
				checkMulDiv(t, x, mul, div)
			}
		}
	}
}

func FuzzMulDiv(f *testing.F) {
	f.Add(int64(2), int64(math.MaxInt64), int64(1_000_000_000))
	f.Add(int64(math.MinInt64), int64(-1), int64(1))
	f.Add(int64(math.MinInt64), int64(math.MinInt64), int64(math.MinInt64))
	f.Add(int64(0), int64(0), int64(0))
	f.Fuzz(checkMulDiv)
}

func checkMulDiv(t *testing.T, x, mul, div int64) {
	t.Helper()
	got, ok := mathutil.MulDiv(x, mul, div)
	if div == 0 {
		if ok {
			t.Errorf("MulDiv(%d, %d, 0) = (%d, true), want failure", x, mul, got)
		}
		return
	}
	want := new(big.Int).Mul(big.NewInt(x), big.NewInt(mul))
	want.Quo(want, big.NewInt(div))
	if ok != want.IsInt64() {
		t.Errorf("MulDiv(%d, %d, %d) success = %t, want %t", x, mul, div, ok, want.IsInt64())
	}
	if ok && want.IsInt64() && got != want.Int64() {
		t.Errorf("MulDiv(%d, %d, %d) = %d, want %d", x, mul, div, got, want.Int64())
	}
}

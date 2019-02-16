// Copyright 2019 The Oto Authors
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

package mux_test

import (
	"bytes"
	"io"
	"reflect"
	"testing"

	"github.com/hajimehoshi/oto/internal/mux"
)

func TestMux8Bits(t *testing.T) {
	cases := []struct {
		Sources [][]byte
		Out     []byte
	}{
		{
			Sources: [][]byte{
				{128, 129, 130},
				{131, 132, 133},
			},
			Out: []byte{131, 133, 135},
		},
		{
			Sources: [][]byte{
				{128, 129, 130},
				{128, 127, 126},
				{128, 128, 128},
			},
			Out: []byte{128, 128, 128},
		},
	}
	for _, c := range cases {
		m := mux.New(1, 1)
		for _, s := range c.Sources {
			m.AddSource(bytes.NewReader(s))
		}

		buf := make([]byte, len(c.Sources[0]))
		if _, err := io.ReadFull(m, buf); err != nil {
			t.Fatal(err)
		}

		got := buf
		want := c.Out
		if !bytes.Equal(got, want) {
			t.Errorf("got: %v, want: %v", got, want)
		}
		m.Close()
	}
}

func int16sToBytes(buf []int16) []byte {
	bs := make([]byte, len(buf)*2)
	for i, b := range buf {
		bs[2*i] = byte(b)
		bs[2*i+1] = byte(b >> 8)
	}
	return bs
}

func bytesToInt16s(buf []byte) []int16 {
	is := make([]int16, len(buf)/2)
	for i := 0; i < len(is); i++ {
		is[i] = int16(buf[2*i]) | (int16(buf[2*i+1]) << 8)
	}
	return is
}

func TestMux16Bits(t *testing.T) {
	cases := []struct {
		Sources [][]int16
		Out     []int16
	}{
		{
			Sources: [][]int16{
				{0, 1, 2},
				{3, 4, 5},
			},
			Out: []int16{3, 5, 7},
		},
		{
			Sources: [][]int16{
				{0, 1, 2},
				{0, -1, -2},
				{0, 0, 0},
			},
			Out: []int16{0, 0, 0},
		},
	}
	for _, c := range cases {
		m := mux.New(1, 2)
		for _, s := range c.Sources {
			m.AddSource(bytes.NewReader(int16sToBytes(s)))
		}

		buf := make([]byte, len(c.Sources[0])*2)
		if _, err := io.ReadFull(m, buf); err != nil {
			t.Fatal(err)
		}

		got := bytesToInt16s(buf)
		want := c.Out
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got: %v, want: %v", got, want)
		}
		m.Close()
	}
}

func TestNoReader(t *testing.T) {
	m := mux.New(2, 2)
	buf := make([]byte, 4096)
	for i := range buf {
		buf[i] = 0xff
	}

	n, err := io.ReadFull(m, buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(buf) {
		t.Errorf("got: %d, want: %d", n, len(buf))
	}
	if !bytes.Equal(buf, make([]byte, len(buf))) {
		t.Errorf("got: %v, want: %v", buf, make([]byte, len(buf)))
	}
}

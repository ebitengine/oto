// Copyright 2022 The Oto Authors
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

package oto_test

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/hajimehoshi/oto/v2"
)

var theContext *oto.Context

func TestMain(m *testing.M) {
	ctx, ready, err := oto.NewContext(48000, 2, 2)
	if err != nil {
		panic(err)
	}
	<-ready
	theContext = ctx
	os.Exit(m.Run())
}

func TestEmptyPlayer(t *testing.T) {
	bs := bytes.NewReader(make([]byte, 0))
	p := theContext.NewPlayer(bs)
	p.Play()
	for p.IsPlaying() {
		time.Sleep(time.Millisecond)
	}
}

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

	"github.com/ebitengine/oto/v3"
)

var theContext *oto.Context

func TestMain(m *testing.M) {
	op := &oto.NewContextOptions{}
	op.SampleRate = 48000
	op.ChannelCount = 2
	op.Format = oto.FormatFloat32LE
	ctx, ready, err := oto.NewContext(op)
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

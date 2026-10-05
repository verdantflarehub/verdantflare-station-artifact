package strictjson

import (
	"strings"
	"testing"
)

func TestExactShape(t *testing.T) {
	type nested struct {
		Kind     string `json:"kind"`
		Optional string `json:"optional,omitempty"`
	}
	type request struct {
		Count  int64    `json:"count"`
		Source nested   `json:"source"`
		Refs   []string `json:"refs"`
	}
	for _, input := range []string{
		`{"count":0,"source":{"kind":"edit"},"refs":[]}`,
		`{"refs":["a"],"source":{"optional":"x","kind":"edit"},"count":12}`,
	} {
		var v request
		if err := Decode(strings.NewReader(input), 1024, &v); err != nil {
			t.Fatalf("valid: %s: %v", input, err)
		}
	}
	for _, input := range []string{
		`{"source":{"kind":"edit"},"refs":[]}`,
		`{"count":0,"Count":1,"source":{"kind":"edit"},"refs":[]}`,
		`{"count":0,"count":1,"source":{"kind":"edit"},"refs":[]}`,
		`{"count":0,"source":{"Kind":"edit"},"refs":[]}`,
		`{"count":0,"source":{"kind":"edit","optional":""},"refs":[]}`,
		`{"count":0,"source":{"kind":"edit","optional":null},"refs":[]}`,
		`{"count":0,"source":{"kind":"edit"},"refs":[null]}`,
		`{"count":0,"source":{"kind":"edit"},"refs":null}`,
		`{"count":0,"source":{"kind":"edit"},"refs":[],"extra":0}`,
		`{"count":0.5,"source":{"kind":"edit"},"refs":[]}`,
		`{"count":0,"source":{"kind":"edit"},"refs":[]} {}`,
		`{"count":0,"source":{"kind":"edit","k\u0069nd":"import"},"refs":[]}`,
		"{\"count\":0,\"source\":{\"kind\":\"\xff\"},\"refs\":[]}",
	} {
		var v request
		if Decode(strings.NewReader(input), 1024, &v) == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	var v request
	if Decode(strings.NewReader(strings.Repeat(" ", 1025)), 1024, &v) == nil {
		t.Fatal("accepted oversized input")
	}
}

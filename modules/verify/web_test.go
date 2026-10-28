package verify

import (
	"context"
	"encoding/json"
	"testing"

	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
)

func TestHandleStateCacheAndRun(t *testing.T) {
	seedConfig(t)
	svc := newService()
	svc.det = &fakeDetector{res: scored(), build: "built-1", api: "chat_completions"}

	out, err := svc.handle(webapi.ExtensionRequest{Context: context.Background(), Body: json.RawMessage(`{"action":"run","provider":"relay","model":"model-x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	e, ok := out.(*Entry)
	if !ok || e.Match != MatchOK {
		t.Fatalf("run returned %T %+v", out, out)
	}

	out, err = svc.handle(webapi.ExtensionRequest{Context: context.Background(), Body: json.RawMessage(`{"action":"cache","provider":"relay"}`)})
	if err != nil {
		t.Fatal(err)
	}
	doc, ok := out.(*cacheDoc)
	if !ok || len(doc.Entries) != 1 || doc.Entries[0].Model != "model-x" {
		t.Fatalf("cache returned %T %+v", out, out)
	}

	out, err = svc.handle(webapi.ExtensionRequest{Context: context.Background(), Body: json.RawMessage(`{"action":"state"}`)})
	if err != nil {
		t.Fatal(err)
	}
	st, ok := out.(State)
	if !ok || len(st.Entries) != 1 || len(st.Profiles) != 1 {
		t.Fatalf("state returned %T %+v", out, out)
	}
}

func TestHandleRejectsUnknownActionAndBadJSON(t *testing.T) {
	seedConfig(t)
	svc := newService()
	if _, err := svc.handle(webapi.ExtensionRequest{Body: json.RawMessage(`{"action":"explode"}`)}); err == nil {
		t.Fatal("unknown action should error")
	}
	if _, err := svc.handle(webapi.ExtensionRequest{Body: json.RawMessage(`{`)}); err == nil {
		t.Fatal("bad JSON should error")
	}
}

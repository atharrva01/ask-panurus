package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbedBatchesAndRetries(t *testing.T) {
	var calls, failures int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/info":
			json.NewEncoder(w).Encode(Info{ModelID: "test/model", MaxInputLength: 8192})
		case "/embed":
			calls++
			if failures == 0 {
				failures++
				http.Error(w, "busy", http.StatusServiceUnavailable)
				return
			}
			var req struct{ Inputs []string }
			json.NewDecoder(r.Body).Decode(&req)
			out := make([][]float32, len(req.Inputs))
			for i, s := range req.Inputs {
				out[i] = []float32{float32(len(s))}
			}
			json.NewEncoder(w).Encode(out)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	c.BatchSize = 2
	info, err := c.Info(context.Background())
	if err != nil || info.ModelID != "test/model" {
		t.Fatalf("info = %+v, %v", info, err)
	}
	vecs, err := c.Embed(context.Background(), []string{"a", "bb", "ccc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 3 || vecs[2][0] != 3 {
		t.Fatalf("vecs = %v", vecs)
	}
	if calls != 3 { // one failed attempt, then two batches
		t.Errorf("calls = %d, want 3", calls)
	}
}

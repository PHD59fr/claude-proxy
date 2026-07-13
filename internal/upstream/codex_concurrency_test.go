package upstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestCodexClientConcurrentTokenUpdates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer " {
			t.Error("request used an empty token")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	client := NewCodexClient(server.URL, "initial", "account", time.Second)
	const requests = 50

	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Do(context.Background(), http.MethodPost, "/", nil)
			if err != nil {
				t.Errorf("Do() error = %v", err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
	}
	for i := 0; i < requests; i++ {
		client.UpdateToken("rotated-token")
	}
	wg.Wait()
}

func TestRouterConcurrentCodexPublication(t *testing.T) {
	router := NewRouter(nil, nil, time.Second)
	const updates = 100

	var wg sync.WaitGroup
	for i := 0; i < updates; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = router.Codex()
		}()
	}
	for i := 0; i < updates; i++ {
		router.SetCodex(&CodexClient{})
	}
	wg.Wait()
}

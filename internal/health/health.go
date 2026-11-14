// Package health provides /healthz (process alive) and /readyz (dependencies reachable).
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Check pings one dependency.
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
}

// Handler serves /healthz and /readyz for the given checks.
func Register(mux *http.ServeMux, checks []Check) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		res := map[string]string{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		ok := true
		for _, c := range checks {
			wg.Add(1)
			go func(c Check) {
				defer wg.Done()
				err := c.Fn(ctx)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					ok = false
					res[c.Name] = err.Error()
				} else {
					res[c.Name] = "ok"
				}
			}(c)
		}
		wg.Wait()
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(res)
	})
}

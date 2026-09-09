// modelproxy answers the Anthropic Messages API and forwards it upstream
// with the model swapped.
//
// It exists for one measurement. Comparing two harnesses only means
// something if they run the same model over the same endpoint, and Claude
// Code cannot be pointed at an arbitrary one: it validates the model name
// against its own catalog before making any HTTP call, and refuses an
// unknown id outright — setting ANTHROPIC_BASE_URL is not enough. Give it
// a name it knows, swap the name here, and both harnesses reach the same
// model.
//
//	modelproxy -addr :8820 -model inception/mercury-2.5 \
//	           -upstream https://openrouter.ai/api/v1 -key $OPENROUTER_API_KEY
//
//	ANTHROPIC_BASE_URL=http://127.0.0.1:8820 ANTHROPIC_AUTH_TOKEN=anything claude -p '...'
//
// This is a benchmarking aid, not a gateway: no retries, no rate limiting,
// no auth of its own. Bind it to localhost.
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8820", "address to listen on")
	model := flag.String("model", "", "model to substitute into every request (required)")
	upstream := flag.String("upstream", "https://openrouter.ai/api/v1", "Anthropic-compatible API root")
	key := flag.String("key", "", "bearer token for the upstream (required)")
	quiet := flag.Bool("quiet", false, "do not log requests")
	flag.Parse()
	if *model == "" || *key == "" {
		log.Fatal("modelproxy: -model and -key are required")
	}
	p := &proxy{model: *model, upstream: strings.TrimSuffix(*upstream, "/"), key: *key, quiet: *quiet}
	log.Printf("modelproxy: %s -> %s serving %s", *addr, p.upstream, p.model)
	srv := &http.Server{Addr: *addr, Handler: p, ReadHeaderTimeout: 30 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

type proxy struct {
	model, upstream, key string
	quiet                bool
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "only POST is proxied", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body, was := swapModel(body, p.model)
	if !p.quiet {
		log.Printf("%s  %s -> %s", r.URL.Path, was, p.model)
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, p.upstream+joinPath(p.upstream, r.URL.RequestURI()), strings.NewReader(string(body)))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("anthropic-version", header(r, "anthropic-version", "2023-06-01"))
	if b := r.Header.Get("anthropic-beta"); b != "" {
		req.Header.Set("anthropic-beta", b)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	// Stream, so a server-sent event reaches the client when it is sent
	// rather than when the response ends.
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 8192)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// joinPath avoids doubling a shared /v1: Claude Code posts to
// /v1/messages?beta=true while an upstream root usually already ends in
// /v1, and the concatenation would 404.
func joinPath(upstream, uri string) string {
	if strings.HasSuffix(upstream, "/v1") && strings.HasPrefix(uri, "/v1/") {
		return uri[len("/v1"):]
	}
	return uri
}

// swapModel replaces the model field and reports what was there. A body it
// cannot parse is forwarded untouched, so anything unexpected still
// reaches the upstream and fails there rather than here.
func swapModel(body []byte, model string) ([]byte, string) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(body, &obj) != nil {
		return body, "(unparsed)"
	}
	was := ""
	if raw, ok := obj["model"]; ok {
		_ = json.Unmarshal(raw, &was)
	}
	enc, err := json.Marshal(model)
	if err != nil {
		return body, was
	}
	obj["model"] = enc
	// Claude Code sends metadata some upstreams reject as an unknown field.
	delete(obj, "metadata")
	out, err := json.Marshal(obj)
	if err != nil {
		return body, was
	}
	return out, was
}

func header(r *http.Request, name, fallback string) string {
	if v := r.Header.Get(name); v != "" {
		return v
	}
	return fallback
}

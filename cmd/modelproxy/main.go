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
//
// With -meter <path> it also appends one JSON object per line (JSONL) for
// every proxied request: when, the path, the model asked for and the model
// swapped in, the upstream status, elapsed milliseconds, and the token
// usage the provider reported (read out of the stream as it passes, never
// buffered whole). That log is the independent accounting a harness's own
// telemetry can be checked against. Metering observes only: a failure to
// parse or write never fails or delays a request.
package main

import (
	"bytes"
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
	meterPath := flag.String("meter", "", "append per-request usage records (JSONL) to this file (optional)")
	quiet := flag.Bool("quiet", false, "do not log requests")
	flag.Parse()
	if *model == "" || *key == "" {
		log.Fatal("modelproxy: -model and -key are required")
	}
	p := &proxy{model: *model, upstream: strings.TrimSuffix(*upstream, "/"), key: *key, quiet: *quiet}
	if *meterPath != "" {
		m, err := openMeter(*meterPath)
		if err != nil {
			log.Fatal("modelproxy: meter: ", err)
		}
		defer m.close()
		p.meter = m
	}
	log.Printf("modelproxy: %s -> %s serving %s", *addr, p.upstream, p.model)
	srv := &http.Server{Addr: *addr, Handler: p, ReadHeaderTimeout: 30 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

type proxy struct {
	model, upstream, key string
	quiet                bool
	meter                *meter
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "only POST is proxied", http.StatusMethodNotAllowed)
		return
	}
	start := time.Now()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body, was := swapModel(body, p.model)
	if !p.quiet {
		log.Printf("%s  %s -> %s", r.URL.Path, was, p.model)
	}
	// The record is filled in as the answer comes back and written however
	// the request ends: an upstream that never responded is still a proxied
	// request, with status 0 and no usage.
	var rec *meterRecord
	if p.meter != nil {
		rec = &meterRecord{Time: start, Path: r.URL.Path, Model: was, Swapped: p.model}
		defer func() {
			rec.ElapsedMS = time.Since(start).Milliseconds()
			p.meter.record(*rec)
		}()
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, p.upstream+joinPath(p.upstream, r.URL.RequestURI()), bytes.NewReader(body))
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
	if rec != nil {
		rec.Status = resp.StatusCode
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	if rec != nil && !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		// A plain JSON answer is small enough to hold: read it, take its
		// usage, pass it on.
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return
		}
		rec.Usage = parseBodyUsage(body)
		_, _ = w.Write(body)
		return
	}
	// Stream, so a server-sent event reaches the client when it is sent
	// rather than when the response ends. The scanner reads the same bytes
	// as they go past; it never touches what is forwarded.
	var scan *sseUsage
	if rec != nil {
		scan = &sseUsage{}
	}
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 8192)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if scan != nil {
				scan.feed(buf[:n])
			}
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	if scan != nil {
		scan.finish()
		rec.Usage = scan.usage
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

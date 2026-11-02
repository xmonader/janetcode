package effbench

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultUpstream is the ai02 broker. Live runs never default elsewhere.
const DefaultUpstream = "https://api.ai02.ourhero.ai"

// UpstreamFromEnv reads the ai02 broker URL and key from AIBROKER_AI02_*.
func UpstreamFromEnv() (base, key string, err error) {
	base = strings.TrimSpace(os.Getenv("AIBROKER_AI02_BASE_URL"))
	if base == "" {
		base = DefaultUpstream
	}
	key = strings.TrimSpace(os.Getenv("AIBROKER_AI02_API_KEY"))
	if key == "" {
		return "", "", fmt.Errorf("AIBROKER_AI02_API_KEY is not set (live runs use the ai02 broker only)")
	}
	return base, key, CheckUpstream(base)
}

// CheckUpstream refuses OpenRouter: the benchmark is pinned to the broker.
func CheckUpstream(base string) error {
	if strings.Contains(strings.ToLower(base), "openrouter") {
		return fmt.Errorf("upstream %q: OpenRouter is not allowed, use the ai02 broker", base)
	}
	return nil
}

// Proxy is a pass-through for OpenAI-compatible and Anthropic-compatible
// requests that meters each one. It never swaps the model: what was asked and
// what answered are both recorded, so a gateway that swaps shows up.
type Proxy struct {
	Upstream string // base URL, with or without a trailing /v1
	Key      string // injected upstream; the agent never sees it
	Log      *Log
	Client   *http.Client
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	start := time.Now()
	rec := Record{Time: start, Path: r.URL.Path, API: apiOf(r.URL.Path)}
	var obj map[string]json.RawMessage
	if r.Method == http.MethodPost && json.Unmarshal(body, &obj) == nil {
		_ = json.Unmarshal(obj["model"], &rec.Asked)
		_ = json.Unmarshal(obj["stream"], &rec.Stream)
		// Streamed OpenAI answers carry usage only when asked.
		if rec.API == "openai" && rec.Stream {
			var so map[string]any
			if json.Unmarshal(obj["stream_options"], &so) != nil || so == nil {
				so = map[string]any{}
			}
			if so["include_usage"] != true {
				so["include_usage"] = true
				obj["stream_options"], _ = json.Marshal(so)
				if nb, err := json.Marshal(obj); err == nil {
					body = nb
					rec.BodyChanged = "stream_options.include_usage"
				}
			}
		}
	}
	uri := r.URL.RequestURI()
	base := strings.TrimSuffix(p.Upstream, "/")
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(uri, "/v1/") {
		uri = uri[len("/v1"):]
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, base+uri, bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for k, v := range r.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Authorization", "X-Api-Key", "Content-Length", "Accept-Encoding", "Host":
		default:
			req.Header[k] = v
		}
	}
	req.Header.Set("Authorization", "Bearer "+p.Key)
	if rec.API == "anthropic" {
		req.Header.Set("x-api-key", p.Key)
	}
	cl := p.Client
	if cl == nil {
		cl = http.DefaultClient
	}
	resp, err := cl.Do(req)
	if err != nil {
		rec.ElapsedMS = time.Since(start).Milliseconds()
		p.Log.Add(rec)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	rec.Status = resp.StatusCode
	for k, v := range resp.Header {
		if http.CanonicalHeaderKey(k) != "Content-Length" {
			w.Header()[k] = v
		}
	}
	w.WriteHeader(resp.StatusCode)
	var scan usageScan
	streamed := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
	var whole bytes.Buffer
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 8192)
	first := true
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if first {
				rec.TTFBMS = time.Since(start).Milliseconds()
				first = false
			}
			if streamed {
				scan.feed(buf[:n])
			} else {
				whole.Write(buf[:n])
			}
			if _, werr := w.Write(buf[:n]); werr != nil {
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	if streamed {
		scan.finish()
	} else {
		scan.object(bytes.TrimSpace(whole.Bytes()))
	}
	rec.ElapsedMS = time.Since(start).Milliseconds()
	rec.Served, rec.Input, rec.Cached, rec.CacheWrite, rec.Output = scan.served, scan.in, scan.cached, scan.write, scan.out
	p.Log.Add(rec)
}

package main

// Owned product-Compose PID acceptance. This binary is only in the fixture image.
import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const pidsProbePython = `import os,sys,time,signal,resource,urllib.parse,ssl,socket,http.client
base,tag,phase,wanted=sys.argv[1:]
wanted=int(wanted)
children=[]
blocked=False
limit=resource.getrlimit(resource.RLIMIT_NPROC)[0]
try:
 for _ in range(wanted if wanted>=0 else 512):
  try: pid=os.fork()
  except OSError:
   blocked=True
   break
  if pid==0:
   time.sleep(45)
   os._exit(0)
  children.append(pid)
 query=urllib.parse.urlencode(dict(tag=tag,phase=phase,children=len(children),blocked=int(blocked),limit=limit))
 target=urllib.parse.urlsplit(base)
 proxy=urllib.parse.urlsplit(os.environ['HTTPS_PROXY'])
 conn=socket.create_connection((proxy.hostname,proxy.port),timeout=30)
 authority=target.netloc
 conn.sendall(('CONNECT '+authority+' HTTP/1.1\r\nHost: '+authority+'\r\n\r\n').encode())
 header=b''
 while not header.endswith(b'\r\n\r\n'):
  part=conn.recv(1)
  if not part: raise RuntimeError('owned proxy closed')
  header+=part
 if not header.startswith(b'HTTP/1.1 200 '): raise RuntimeError('owned CONNECT rejected')
 with ssl.create_default_context().wrap_socket(conn,server_hostname=target.hostname) as tls:
  tls.sendall(('GET /rest/products/pids-hold?'+query+' HTTP/1.1\r\nHost: '+authority+'\r\nAccept-Encoding: identity\r\nConnection: close\r\n\r\n').encode())
  response=http.client.HTTPResponse(tls)
  response.begin()
  if response.status!=200: raise RuntimeError('owned target rejected')
  response.read()
finally:
 for pid in children:
  try: os.kill(pid,signal.SIGKILL)
  except ProcessLookupError: pass
 for pid in children:
  try: os.waitpid(pid,0)
  except ChildProcessError: pass
print('owned-pids-complete')`

func pidsModel(target string, forks int) http.Handler {
	var mu sync.Mutex
	goals, plans := 0, 0
	workers := map[string]int{}
	ordinals := map[string]int{}
	intentPattern := regexp.MustCompile(`Intent id: ([0-9]+)`)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req) != nil {
			http.Error(w, "invalid_owned_request", 400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		names := map[string]bool{}
		for _, t := range req.Tools {
			names[t.Function.Name] = true
		}
		message := map[string]any{"role": "assistant", "content": "Owned PID fixture is finished."}
		finish := "stop"
		tool := func(name, id string, args any) {
			raw, _ := json.Marshal(args)
			message = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}}}
			finish = "tool_calls"
		}
		switch {
		case names["set_goals"] && !names["add_intent"] && !names["Bash"]:
			goals++
			if goals == 1 {
				tool("set_goals", "pids-goal", map[string]any{"goals": []any{map[string]any{"text": "Inspect the owned PID isolation fixture at " + target, "vulnclass": "business_logic"}}})
			}
		case names["add_intent"] && !names["Bash"]:
			plans++
			if plans == 1 {
				intents := []any{}
				for i := 1; i <= 3; i++ {
					intents = append(intents, map[string]any{"summary": fmt.Sprintf("Owned PID isolation intent %d at %s", i, target), "priority": 5})
				}
				tool("add_intent", "pids-intents", map[string]any{"intents": intents})
			} else {
				time.Sleep(100 * time.Millisecond)
			}
		case names["Bash"]:
			id := ""
			for _, m := range req.Messages {
				if v := intentPattern.FindStringSubmatch(string(m.Content)); len(v) > 1 {
					id = v[1]
					break
				}
			}
			if id == "" {
				http.Error(w, "owned_intent_missing", 500)
				return
			}
			if ordinals[id] == 0 {
				ordinals[id] = len(ordinals) + 1
			}
			workers[id]++
			phase := workers[id]
			if phase <= 2 {
				count := forks
				if phase == 1 && ordinals[id] == 3 {
					count = 0
				}
				quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
				command := "python3 -c " + quote(pidsProbePython) + " " + quote(target) + " " + id + " " + strconv.Itoa(phase) + " " + strconv.Itoa(count)
				tool("Bash", fmt.Sprintf("pids-%s-%d", id, phase), map[string]any{"command": command, "timeout_ms": 40000})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "owned-pids", "object": "chat.completion", "model": req.Model, "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 10, "total_tokens": 20}})
	})
}

func servePidsTarget() error {
	token, err := os.ReadFile("/target-tls/control-token")
	if err != nil {
		return err
	}
	var mu sync.Mutex
	rows := map[string]map[string]string{}
	gates := map[string]chan struct{}{"1": make(chan struct{}), "2": make(chan struct{})}
	released := map[string]bool{}
	heavyReady := make(chan struct{})
	var heavyOnce sync.Once
	control := http.NewServeMux()
	authorized := func(r *http.Request) bool {
		return subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Fixture-Control")), token) == 1
	}
	control.HandleFunc("GET /pids-stats", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			http.Error(w, "denied", 403)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(rows)
	})
	control.HandleFunc("POST /pids-release", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			http.Error(w, "denied", 403)
			return
		}
		phase := r.URL.Query().Get("phase")
		mu.Lock()
		defer mu.Unlock()
		if gates[phase] == nil {
			http.Error(w, "invalid_phase", 400)
			return
		}
		if !released[phase] {
			close(gates[phase])
			released[phase] = true
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"released": true})
	})
	go func() { must(http.ListenAndServe(":9081", control)) }()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/products/pids-hold" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"owned_pid_fixture":true}`)
			return
		}
		q := r.URL.Query()
		phase, tag := q.Get("phase"), q.Get("tag")
		if gates[phase] == nil || tag == "" {
			http.Error(w, "invalid_probe", 400)
			return
		}
		row := map[string]string{}
		for _, key := range []string{"phase", "tag", "children", "blocked", "limit"} {
			row[key] = q.Get(key)
		}
		mu.Lock()
		rows[phase+":"+tag] = row
		heavy := 0
		for _, seen := range rows {
			if seen["phase"] == "1" && seen["children"] != "0" {
				heavy++
			}
		}
		if heavy >= 2 {
			heavyOnce.Do(func() { close(heavyReady) })
		}
		mu.Unlock()
		// The innocent phase-one command must finish while two heavy calls remain.
		if phase == "1" && q.Get("children") == "0" {
			select {
			case <-heavyReady:
			case <-r.Context().Done():
				return
			}
		} else {
			select {
			case <-gates[phase]:
			case <-r.Context().Done():
				return
			}
		}
		io.WriteString(w, "owned-pids-response")
	})
	server := http.Server{Addr: ":9443", Handler: handler}
	return server.ListenAndServeTLS("/target-tls/leaf.pem", "/target-tls/key.pem")
}

func pidsModelHandler(target string, forks int) error {
	if _, err := url.ParseRequestURI(target); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("POST /v1/chat/completions", pidsModel(target, forks))
	return http.ListenAndServe(":8080", mux)
}

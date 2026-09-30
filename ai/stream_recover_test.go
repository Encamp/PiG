package ai

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// goroutineStart is one `go` statement or `x.Go(func() {...})` call in the package's production files.
type goroutineStart struct {
	file, function string
	body           *ast.BlockStmt
	problem        string
	pos            token.Position
}

// streamGoroutineExemptions lists the goroutine starts that serve no stream: authentication, OAuth callback servers and model-store operations. The key is file and enclosing function; the value is how many starts that function holds.
var streamGoroutineExemptions = map[[2]string]int{
	{"auth_providers.go", "oauthRefresh"}:                                1,
	{"auth_reload.go", "AuthStorage.readLatest"}:                         1,
	{"auth_store.go", "InMemoryAuthStorage.withLockAsync"}:               1,
	{"images_models.go", "ImagesModels.Refresh"}:                         1,
	{"in_memory_credential_store.go", "InMemoryCredentialStore.enqueue"}: 1,
	{"models_runtime.go", "awaitModelsOperation"}:                        1,
	{"models_runtime_auth.go", "Models.GetAvailable"}:                    1,
	{"models_runtime_auth.go", "Models.Login"}:                           1,
	{"models_runtime_refresh.go", "Models.Refresh"}:                      2,
	{"models_runtime_refresh.go", "Models.publishProviderModels"}:        1,
	{"models_store_read.go", "FileModelsStore.readLatest"}:               1,
	{"oauth_anthropic.go", "startCallbackServer"}:                        1,
	{"oauth_anthropic.go", "LoginAnthropic"}:                             2,
	{"oauth_openai_codex.go", "startCodexCallbackServer"}:                1,
	{"oauth_openai_codex.go", "LoginOpenAICodex"}:                        1,
	{"oauth_openrouter.go", "startOpenRouterCallbackServer"}:             2,
	{"oauth_openrouter.go", "LoginOpenRouter"}:                           1,
	{"oauth_radius.go", "RadiusOAuth.startCallbackServer"}:               2,
}

// TestStreamGoroutinesDeferRecoverFirst requires every goroutine the package starts to defer recoverStream as its first statement, so that a panic ends one stream instead of the process. A start that serves no stream is listed in streamGoroutineExemptions; a stale entry fails, and so does an entry whose function handles a stream.
func TestStreamGoroutinesDeferRecoverFirst(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var parsed []*ast.File
	decls := map[string][]*ast.FuncDecl{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, file)
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				decls[fn.Name.Name] = append(decls[fn.Name.Name], fn)
			}
		}
	}

	var starts []goroutineStart
	enclosing := map[[2]string]*ast.FuncDecl{}
	for _, file := range parsed {
		fileName := filepath.Base(fset.Position(file.Pos()).Filename)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			enclosing[[2]string{fileName, funcDeclName(fn)}] = fn
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				start := goroutineStart{file: fileName, function: funcDeclName(fn)}
				switch node := node.(type) {
				case *ast.GoStmt:
					start.pos = fset.Position(node.Pos())
					start.body, start.problem = goStatementBody(node.Call.Fun, decls)
				case *ast.CallExpr:
					selector, ok := node.Fun.(*ast.SelectorExpr)
					if !ok || selector.Sel.Name != "Go" || len(node.Args) != 1 {
						return true
					}
					literal, ok := node.Args[0].(*ast.FuncLit)
					if !ok {
						return true
					}
					start.pos = fset.Position(node.Pos())
					start.body = literal.Body
				default:
					return true
				}
				starts = append(starts, start)
				return true
			})
		}
	}
	if len(starts) == 0 {
		t.Fatal("found no goroutine starts; the scan is broken")
	}

	unexempted := map[[2]string]int{}
	var failures []string
	for _, start := range starts {
		if start.problem == "" && defersRecoverFirst(start.body) {
			continue
		}
		key := [2]string{start.file, start.function}
		if _, exempt := streamGoroutineExemptions[key]; exempt {
			unexempted[key]++
			continue
		}
		problem := start.problem
		if problem == "" {
			problem = "first statement is not defer recoverStream(...)"
		}
		failures = append(failures, fmt.Sprintf("%s (%s): %s", start.pos, start.function, problem))
	}
	for key, want := range streamGoroutineExemptions {
		if got := unexempted[key]; got != want {
			failures = append(failures, fmt.Sprintf("exemption %s %s lists %d unrecovered starts, found %d", key[0], key[1], want, got))
		}
		if fn := enclosing[key]; fn != nil && handlesStream(fn) {
			failures = append(failures, fmt.Sprintf("exemption %s %s handles a stream", key[0], key[1]))
		}
	}
	slices.Sort(failures)
	for _, failure := range failures {
		t.Error(failure)
	}
}

func funcDeclName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	receiver := fn.Recv.List[0].Type
	if star, ok := receiver.(*ast.StarExpr); ok {
		receiver = star.X
	}
	if index, ok := receiver.(*ast.IndexExpr); ok {
		receiver = index.X
	}
	if ident, ok := receiver.(*ast.Ident); ok {
		return ident.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}

// goStatementBody resolves the function a go statement starts: a literal, or the one declaration of that name. An ambiguous name or a function value must be wrapped in a literal at the site.
func goStatementBody(fun ast.Expr, decls map[string][]*ast.FuncDecl) (*ast.BlockStmt, string) {
	var name string
	switch fun := fun.(type) {
	case *ast.FuncLit:
		return fun.Body, ""
	case *ast.Ident:
		name = fun.Name
	case *ast.SelectorExpr:
		name = fun.Sel.Name
	default:
		return nil, fmt.Sprintf("go statement starts %T; wrap it in a func literal", fun)
	}
	switch found := decls[name]; len(found) {
	case 0:
		return nil, fmt.Sprintf("go %s starts a function value; wrap it in a func literal", name)
	case 1:
		return found[0].Body, ""
	default:
		return nil, fmt.Sprintf("go %s names %d declarations; wrap it in a func literal", name, len(found))
	}
}

func defersRecoverFirst(body *ast.BlockStmt) bool {
	if body == nil || len(body.List) == 0 {
		return false
	}
	deferred, ok := body.List[0].(*ast.DeferStmt)
	if !ok {
		return false
	}
	callee, ok := deferred.Call.Fun.(*ast.Ident)
	return ok && callee.Name == "recoverStream"
}

// handlesStream reports whether a function touches an assistant event stream or its builder.
func handlesStream(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn, func(node ast.Node) bool {
		if ident, ok := node.(*ast.Ident); ok && (ident.Name == "AssistantMessageEventStream" || ident.Name == "assistantStreamBuilder") {
			found = true
		}
		return !found
	})
	return found
}

// streamPanicFamily is one provider family's minimal successful stream.
type streamPanicFamily struct {
	api      API
	provider string
	body     string
	make     func(baseURL string) Provider
}

func streamPanicFamilies() []streamPanicFamily {
	return []streamPanicFamily{
		{
			api: APIAnthropicMessages, provider: "anthropic",
			body: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			make: func(baseURL string) Provider {
				return NewAnthropicProvider(AnthropicConfig{APIKey: "test", Model: "claude-test", BaseURL: baseURL, ProviderID: "anthropic"})
			},
		},
		{
			api: APIOpenAIResponses, provider: "openai",
			body: "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg-1\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
				"data: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello\"}\n\n" +
				"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg-1\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
			make: func(baseURL string) Provider {
				return NewOpenAIResponsesProvider(OpenAIResponsesConfig{APIKey: "test", Model: "gpt-test", BaseURL: baseURL, ProviderID: "openai"})
			},
		},
		{
			api: APIOpenAICompletions, provider: "openai",
			body: "data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hel\"},\"finish_reason\":null}]}\n\n" +
				"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: [DONE]\n\n",
			make: func(baseURL string) Provider {
				return NewOpenAIProvider(OpenAIConfig{APIKey: "test", Model: "gpt-test", BaseURL: baseURL, ProviderID: "openai"})
			},
		},
		{
			api: APIGoogleGenerativeAI, provider: "google",
			body: "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hel\"}]}}]}\n\n" +
				"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"lo\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}\n\n",
			make: func(baseURL string) Provider {
				return NewGoogleProvider(GoogleConfig{APIKey: "test", Model: "gemini-test", BaseURL: baseURL, ProviderID: "google"})
			},
		},
	}
}

func streamPanicTranscript() TranscriptContext {
	return NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi"), Timestamp: 1}}})
}

// drainPanicStream iterates the stream and requests its result within a deadline, and returns the error events it saw.
func drainPanicStream(t *testing.T, stream *AssistantMessageEventStream) ([]ErrorEvent, *AssistantMessage) {
	t.Helper()
	type drained struct {
		errors []ErrorEvent
		result *AssistantMessage
	}
	done := make(chan drained, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	go func() {
		var seen []ErrorEvent
		for event := range stream.Events(ctx) {
			if failure, ok := event.(ErrorEvent); ok {
				seen = append(seen, failure)
			}
		}
		done <- drained{errors: seen, result: stream.Result()}
	}()
	select {
	case out := <-done:
		return out.errors, out.result
	case <-ctx.Done():
		t.Fatal("stream did not end after the panic")
		return nil, nil
	}
}

// TestStreamDecoderPanicEndsStream panics inside each provider family's stream goroutine, at its second event, and requires the stream to end with one error event that names the provider and the panic.
func TestStreamDecoderPanicEndsStream(t *testing.T) {
	for _, family := range streamPanicFamilies() {
		t.Run(string(family.api), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, family.body)
			}))
			t.Cleanup(server.Close)
			var events atomic.Int32
			hook := func(AssistantMessageEvent) {
				if events.Add(1) == 2 {
					panic("synthetic decoder fault")
				}
			}
			ctx := context.WithValue(t.Context(), streamDecoderHookKey{}, hook)
			stream, err := family.make(server.URL).Stream(ctx, streamPanicTranscript(), StreamOptions{})
			if err != nil {
				t.Fatal(err)
			}
			failures, result := drainPanicStream(t, stream)
			want := fmt.Sprintf("provider %s goroutine panicked: synthetic decoder fault", family.provider)
			if len(failures) != 1 {
				t.Fatalf("error events = %d, want 1 (result %#v)", len(failures), result)
			}
			failure := failures[0]
			if failure.Reason != StopReasonError || failure.Error.ErrorMessage != want {
				t.Fatalf("error event = %q %q, want %q %q", failure.Reason, failure.Error.ErrorMessage, StopReasonError, want)
			}
			if failure.Error.API != family.api || failure.Error.Provider != family.provider || failure.Error.Model == "" {
				t.Fatalf("error identity = %q/%q/%q, want %q/%q", failure.Error.API, failure.Error.Provider, failure.Error.Model, family.api, family.provider)
			}
			if result == nil || result.StopReason != StopReasonError || result.ErrorMessage != want {
				t.Fatalf("result = %#v, want the panic error", result)
			}
		})
	}
}

// panickingBody returns one valid SSE chunk, then panics on the next read.
type panickingBody struct {
	first []byte
	reads atomic.Int32
}

func (body *panickingBody) Read(buffer []byte) (int, error) {
	if body.reads.Add(1) == 1 {
		return copy(buffer, body.first), nil
	}
	panic("synthetic body fault")
}

func (body *panickingBody) Close() error { return nil }

// TestStreamBodyReaderPanicEndsStream panics inside the goroutine that reads a caller-supplied fetch client's response body, after one valid chunk, and requires the stream to end with the panic.
func TestStreamBodyReaderPanicEndsStream(t *testing.T) {
	for _, family := range streamPanicFamilies() {
		if family.api == APIGoogleGenerativeAI {
			continue // Google rejects a caller-supplied fetch client before it streams.
		}
		t.Run(string(family.api), func(t *testing.T) {
			first, _, _ := strings.Cut(family.body, "\n\n")
			fetch := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK, ProtoMajor: 1, ProtoMinor: 1, Request: request,
					Header: http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:   &panickingBody{first: []byte(first + "\n\n")},
				}, nil
			})}
			stream, err := family.make("https://example.invalid").Stream(t.Context(), streamPanicTranscript(), StreamOptions{Fetch: fetch})
			if err != nil {
				t.Fatal(err)
			}
			failures, result := drainPanicStream(t, stream)
			if len(failures) != 1 || !strings.Contains(failures[0].Error.ErrorMessage, "goroutine panicked: synthetic body fault") {
				t.Fatalf("error events = %#v, want one naming the panic", failures)
			}
			if result == nil || result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "goroutine panicked") {
				t.Fatalf("result = %#v, want the panic error", result)
			}
		})
	}
}

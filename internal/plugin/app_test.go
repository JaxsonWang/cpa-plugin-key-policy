package plugin

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"cpa-key-policy/internal/policy"
)

func configureTestApp(t *testing.T, rpm int) (*App, string) {
	t.Helper()
	app := NewApp()
	t.Cleanup(app.Shutdown)
	plain := "cpa_plugin_test"
	hash, err := policy.HashKey(plain)
	if err != nil {
		t.Fatal(err)
	}
	yaml := []byte(`
enabled: true
state_file: "` + filepath.ToSlash(filepath.Join(t.TempDir(), "state.json")) + `"
keys:
  - id: team-a
    name: Team A
    enabled: true
    key_hash: "` + hash + `"
    key_preview: "cpa_plu..._test"
    rpm: ` + itoaForTest(rpm) + `
    models:
      - model: gpt-5.4
        billing_mode: tokens
        input_price_per_million: 2
        output_price_per_million: 8
`)
	req, _ := json.Marshal(LifecycleRequest{ConfigYAML: yaml})
	if _, err := app.HandleMethod(MethodPluginReconfigure, req); err != nil {
		t.Fatalf("configure: %v", err)
	}
	return app, plain
}

func itoaForTest(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[i:])
}

func decodeResult[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK {
		t.Fatalf("plugin envelope failed: %+v", envelope.Error)
	}
	var result T
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func pluginHeaders(key string) http.Header {
	return http.Header{"Authorization": []string{"Bearer " + key}}
}

func websocketHeaders(key string) http.Header {
	headers := pluginHeaders(key)
	headers.Set("Connection", "keep-alive, Upgrade")
	headers.Set("Upgrade", "websocket")
	return headers
}

func TestRegistrationUsesRequestInterceptorWithoutModelRouter(t *testing.T) {
	app := NewApp()
	t.Cleanup(app.Shutdown)
	registration := app.registration()
	if !registration.Capabilities.FrontendAuthProvider || !registration.Capabilities.RequestInterceptor || !registration.Capabilities.UsagePlugin {
		t.Fatalf("required capabilities missing: %+v", registration.Capabilities)
	}
	raw, err := json.Marshal(registration)
	if err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{"model_router", "scheduler", "response_interceptor"} {
		if strings.Contains(string(raw), removed) {
			t.Fatalf("removed routing capability %q still registered: %s", removed, raw)
		}
	}
}

func TestHTTPAuthenticationEnforcesExactModel(t *testing.T) {
	app, plain := configureTestApp(t, 60)
	allowed, _ := json.Marshal(FrontendAuthRequest{
		Method:  http.MethodPost,
		Path:    "/v1/responses",
		Headers: pluginHeaders(plain),
		Body:    []byte(`{"model":"gpt-5.4"}`),
	})
	raw, err := app.HandleMethod(MethodFrontendAuthAuthenticate, allowed)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeResult[FrontendAuthResponse](t, raw)
	if !response.Authenticated || response.Principal != "team-a" {
		t.Fatalf("allowed response = %+v", response)
	}
	if _, routed := response.Metadata["target_model"]; routed {
		t.Fatalf("authentication leaked removed routing metadata: %+v", response.Metadata)
	}

	denied, _ := json.Marshal(FrontendAuthRequest{
		Method:  http.MethodPost,
		Path:    "/v1/responses",
		Headers: pluginHeaders(plain),
		Body:    []byte(`{"model":"gpt-5.4-mini"}`),
	})
	raw, err = app.HandleMethod(MethodFrontendAuthAuthenticate, denied)
	if err != nil {
		t.Fatal(err)
	}
	if decodeResult[FrontendAuthResponse](t, raw).Authenticated {
		t.Fatal("unlisted model authenticated")
	}
}

func TestWebSocketHandshakeDefersLimitsToEveryExecutionFrame(t *testing.T) {
	app, plain := configureTestApp(t, 1)
	handshake, _ := json.Marshal(FrontendAuthRequest{
		Method:  http.MethodGet,
		Path:    "/v1/responses",
		Headers: websocketHeaders(plain),
	})
	for i := 0; i < 2; i++ {
		raw, err := app.HandleMethod(MethodFrontendAuthAuthenticate, handshake)
		if err != nil {
			t.Fatal(err)
		}
		if !decodeResult[FrontendAuthResponse](t, raw).Authenticated {
			t.Fatalf("handshake %d consumed or failed policy", i+1)
		}
	}

	frame, _ := json.Marshal(RequestInterceptRequest{
		SourceFormat:   "openai-response",
		RequestedModel: "gpt-5.4",
		Model:          "gpt-5.4",
		Stream:         true,
		Headers:        websocketHeaders(plain),
		Body:           []byte(`{"type":"response.create","model":"gpt-5.4"}`),
	})
	raw, err := app.HandleMethod(MethodRequestInterceptBefore, frame)
	if err != nil {
		t.Fatal(err)
	}
	if response := decodeResult[RequestInterceptResponse](t, raw); response.Terminate {
		t.Fatalf("first frame rejected: %+v", response)
	}

	raw, err = app.HandleMethod(MethodRequestInterceptBefore, frame)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeResult[RequestInterceptResponse](t, raw)
	if !response.Terminate || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second frame = %+v, want 429 termination", response)
	}
	if !strings.Contains(string(response.ResponseBody), "rpm_exceeded") {
		t.Fatalf("missing structured reason: %s", response.ResponseBody)
	}
}

func TestWebSocketInterceptorRejectsUnlistedModelAndIgnoresNativeKeys(t *testing.T) {
	app, plain := configureTestApp(t, 60)
	request := RequestInterceptRequest{
		SourceFormat:   "openai-response",
		RequestedModel: "gpt-5.4-mini",
		Headers:        websocketHeaders(plain),
		Body:           []byte(`{"type":"response.create","model":"gpt-5.4-mini"}`),
	}
	rawRequest, _ := json.Marshal(request)
	raw, err := app.HandleMethod(MethodRequestInterceptBefore, rawRequest)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeResult[RequestInterceptResponse](t, raw)
	if !response.Terminate || response.StatusCode != http.StatusForbidden {
		t.Fatalf("unlisted model = %+v, want 403 termination", response)
	}

	request.Headers = websocketHeaders("native-key-owned-by-cpa")
	rawRequest, _ = json.Marshal(request)
	raw, err = app.HandleMethod(MethodRequestInterceptBefore, rawRequest)
	if err != nil {
		t.Fatal(err)
	}
	if response := decodeResult[RequestInterceptResponse](t, raw); response.Terminate {
		t.Fatalf("plugin intercepted another auth provider's key: %+v", response)
	}
}

func TestWebSocketInterceptorEnforcesBudgetPerExecution(t *testing.T) {
	app, plain := configureTestApp(t, 60)
	key := app.Store().Keys()[0]
	key.DailyLimitUSD = 1
	if err := app.Store().UpsertKey(key, false); err != nil {
		t.Fatal(err)
	}
	if cost := app.Store().RecordUsage("team-a", "gpt-5.4", "gpt-5.4", false, policy.UsageDetail{
		Provider:    "codex",
		InputTokens: 500_000,
	}); cost != 1 {
		t.Fatalf("seed cost = %v, want 1", cost)
	}

	frame, _ := json.Marshal(RequestInterceptRequest{
		RequestedModel: "gpt-5.4",
		Headers:        websocketHeaders(plain),
		Body:           []byte(`{"type":"response.create","model":"gpt-5.4"}`),
	})
	raw, err := app.HandleMethod(MethodRequestInterceptBefore, frame)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeResult[RequestInterceptResponse](t, raw)
	if !response.Terminate || response.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(response.ResponseBody), "daily_exceeded") {
		t.Fatalf("budget response = %+v, want daily-exceeded 429", response)
	}
}

func TestNonWebSocketInterceptorDoesNotDoubleCountRPM(t *testing.T) {
	app, plain := configureTestApp(t, 1)
	auth, _ := json.Marshal(FrontendAuthRequest{
		Method:  http.MethodPost,
		Path:    "/v1/responses",
		Headers: pluginHeaders(plain),
		Body:    []byte(`{"model":"gpt-5.4"}`),
	})
	raw, err := app.HandleMethod(MethodFrontendAuthAuthenticate, auth)
	if err != nil || !decodeResult[FrontendAuthResponse](t, raw).Authenticated {
		t.Fatalf("authentication failed: %v", err)
	}
	intercept, _ := json.Marshal(RequestInterceptRequest{
		RequestedModel: "gpt-5.4",
		Headers:        pluginHeaders(plain),
		Body:           []byte(`{"model":"gpt-5.4"}`),
	})
	raw, err = app.HandleMethod(MethodRequestInterceptBefore, intercept)
	if err != nil {
		t.Fatal(err)
	}
	if response := decodeResult[RequestInterceptResponse](t, raw); response.Terminate {
		t.Fatalf("HTTP interceptor double-enforced policy: %+v", response)
	}
}

func TestUsageUsesActualProviderAndExactModel(t *testing.T) {
	app, _ := configureTestApp(t, 60)
	request, _ := json.Marshal(UsageHandleRequest{
		Provider: "codex",
		Model:    "gpt-5.4",
		Alias:    "gpt-5.4",
		APIKey:   "team-a",
		Detail: UsageDetail{
			InputTokens:  1_000_000,
			OutputTokens: 1_000_000,
		},
	})
	if _, err := app.HandleMethod(MethodUsageHandle, request); err != nil {
		t.Fatal(err)
	}
	key, rows, ok := app.Store().ModelUsageFor("team-a")
	if !ok || len(rows) != 1 || rows[0].Model != "gpt-5.4" {
		t.Fatalf("usage rows = %+v, key = %+v", rows, key)
	}
	if got := app.Store().UsageSummaryFor(key).DailyUSD; got != 10 {
		t.Fatalf("daily cost = %v, want 10", got)
	}
}

func TestManagementRegistrationOmitsRemovedRoutes(t *testing.T) {
	app := NewApp()
	t.Cleanup(app.Shutdown)
	registration := app.managementRegistration()
	for _, route := range registration.Routes {
		for _, removed := range []string{"/aliases", "/classify", "/catalog"} {
			if strings.Contains(route.Path, removed) {
				t.Fatalf("removed route still registered: %+v", route)
			}
		}
	}
}

func TestManagementRegistrationIncludesGlobalUsageReset(t *testing.T) {
	app := NewApp()
	t.Cleanup(app.Shutdown)
	for _, route := range app.managementRegistration().Routes {
		if route.Method == http.MethodPost && route.Path == "/plugins/cpa-key-policy/keys/reset-usage" {
			return
		}
	}
	t.Fatal("global usage reset route was not registered")
}

func TestManagementResetUsageClearsDailyAndWeeklyAndPersists(t *testing.T) {
	app, _ := configureTestApp(t, 60)
	if cost := app.Store().RecordUsage("team-a", "gpt-5.4", "gpt-5.4", false, policy.UsageDetail{
		Provider:    "codex",
		InputTokens: 1_000_000,
	}); cost != 2 {
		t.Fatalf("seed cost = %v, want 2", cost)
	}

	before := app.Store().UsageSummaryFor(app.Store().Keys()[0])
	if before.DailyUSD != 2 || before.WeeklyUSD != 2 {
		t.Fatalf("usage before reset = %+v, want daily and weekly usage", before)
	}

	request, _ := json.Marshal(ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-key-policy/keys/reset-usage",
	})
	raw, err := app.HandleMethod(MethodManagementHandle, request)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeResult[ManagementResponse](t, raw)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("reset response = %+v, body = %s", response, response.Body)
	}

	after := app.Store().UsageSummaryFor(app.Store().Keys()[0])
	if after.DailyUSD != 0 || after.WeeklyUSD != 0 || after.DailyCallCount != 0 || after.WeeklyCallCount != 0 {
		t.Fatalf("usage after reset = %+v, want zero daily and weekly usage", after)
	}
	persisted, err := policy.LoadState(app.Store().StatePath())
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Usage) != 0 {
		t.Fatalf("persisted usage after reset = %+v, want empty", persisted.Usage)
	}
}

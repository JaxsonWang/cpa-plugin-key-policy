package plugin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"cpa-key-policy/internal/plugin/web"
	"cpa-key-policy/internal/policy"
)

type App struct {
	store *policy.Store
}

func NewApp() *App {
	store := policy.NewStore()
	_ = store.Configure(policy.DefaultConfig())
	return &App{store: store}
}

func (a *App) HandleMethod(method string, request []byte) ([]byte, error) {
	return safePluginCall(func() ([]byte, error) {
		return a.handleMethod(method, request)
	})
}

func (a *App) handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case MethodPluginRegister, MethodPluginReconfigure:
		if err := a.configure(request); err != nil {
			return nil, err
		}
		return OKEnvelope(a.registration())
	case MethodFrontendAuthIdentifier:
		return OKEnvelope(IdentifierResponse{Identifier: PluginID})
	case MethodFrontendAuthAuthenticate:
		return a.authenticate(request)
	case MethodRequestInterceptBefore:
		return a.interceptRequestBefore(request)
	case MethodRequestInterceptAfter:
		return OKEnvelope(RequestInterceptResponse{})
	case MethodUsageHandle:
		return a.handleUsage(request)
	case MethodManagementRegister:
		return OKEnvelope(a.managementRegistration())
	case MethodManagementHandle:
		return a.handleManagement(request)
	default:
		return ErrorEnvelope("unknown_method", "unknown method: "+method, http.StatusNotFound), nil
	}
}

func safePluginCall(call func() ([]byte, error)) (response []byte, err error) {
	defer func() {
		if recover() != nil {
			response = nil
			err = errors.New("plugin panic recovered")
		}
	}()
	return call()
}

func (a *App) configure(raw []byte) error {
	var req LifecycleRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return err
		}
	}
	cfg, err := policy.DecodeConfig(req.ConfigYAML)
	if err != nil {
		return err
	}
	if err := a.store.Configure(cfg); err != nil {
		return err
	}
	a.store.StartUsageFlusher()
	return nil
}

func (a *App) Shutdown() {
	a.store.StopUsageFlusher()
}

func (a *App) registration() Registration {
	return Registration{
		SchemaVersion: SchemaVersion,
		Metadata: Metadata{
			Name:             PluginName,
			Version:          Version,
			Author:           "cpa-key-policy",
			GitHubRepository: "https://github.com/JaxsonWang/cpa-plugin-key-policy",
			ConfigFields: []ConfigField{
				{Name: "enabled", Type: "boolean", Description: "Enable or disable this plugin without unloading it."},
				{Name: "state_file", Type: "string", Description: "JSON state file used for key policy changes made through the Management API."},
				{Name: "keys", Type: "array", Description: "Initial downstream key policy list. State file wins after it exists."},
			},
		},
		Capabilities: Capabilities{
			FrontendAuthProvider:          true,
			FrontendAuthProviderExclusive: false,
			RequestInterceptor:            true,
			UsagePlugin:                   true,
			ManagementAPI:                 true,
		},
	}
}

func (a *App) authenticate(raw []byte) ([]byte, error) {
	var req FrontendAuthRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	decision := a.store.AuthenticateKey(req.Headers, req.Query)
	// The models endpoint has no model-execution lifecycle, so its binary
	// permission remains an authentication-time decision. Every model execution
	// is authenticated by key identity here and policy-checked in the request
	// interceptor, which preserves real 403/429 errors for valid legacy keys.
	if policy.IsModelsEndpoint(req.Path) {
		decision = a.store.Authenticate(req.Method, req.Path, req.Headers, req.Query, req.Body)
	} else if !isWebSocketUpgrade(req.Headers) && policy.ExtractAPIKey(req.Headers, nil) == "" && policy.ExtractAPIKey(nil, req.Query) != "" {
		// CPA's request interceptor ABI does not carry query parameters. Keep the
		// existing HTTP query-key enforcement path here rather than authenticating
		// a query key and then accidentally skipping its policy in interception.
		decision = a.store.Authenticate(req.Method, req.Path, req.Headers, req.Query, req.Body)
	}
	if !decision.Known || !decision.Allowed {
		return OKEnvelope(FrontendAuthResponse{Authenticated: false})
	}
	metadata := map[string]string{
		"provider": PluginID,
		"key_id":   decision.KeyID,
	}
	requested := policy.ExtractRequestedModel(req.Path, req.Query, req.Body)
	if requested != "" {
		metadata["requested_model"] = requested
	}
	return OKEnvelope(FrontendAuthResponse{
		Authenticated: true,
		Principal:     decision.Principal,
		Metadata:      metadata,
	})
}

func isWebSocketUpgrade(headers http.Header) bool {
	if !strings.EqualFold(strings.TrimSpace(headers.Get("Upgrade")), "websocket") {
		return false
	}
	for _, token := range strings.Split(headers.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
			return true
		}
	}
	return false
}

func (a *App) interceptRequestBefore(raw []byte) ([]byte, error) {
	var req RequestInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	requested := strings.TrimSpace(req.RequestedModel)
	if requested == "" {
		requested = strings.TrimSpace(req.Model)
	}
	if requested == "" {
		requested = policy.ExtractRequestedModel("", nil, req.Body)
	}
	decision := a.store.AuthorizeModel(req.Headers, nil, requested)
	if !decision.Known || decision.Allowed {
		if decision.Allowed && isPerCallSourceFormat(req.SourceFormat) && decision.Rule.BillingMode == "per_call" {
			model := decision.Rule.Model
			if strings.TrimSpace(model) == "" {
				model = decision.Requested
			}
			a.store.RecordUsage(decision.KeyID, model, model, false, policy.UsageDetail{})
		}
		return OKEnvelope(RequestInterceptResponse{})
	}
	status := http.StatusForbidden
	switch {
	case decision.Reason == "model_required":
		status = http.StatusBadRequest
	case decision.RateLimited || decision.CostLimited:
		status = http.StatusTooManyRequests
	}
	return OKEnvelope(RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      status,
		ResponseHeaders: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		ResponseBody:    policyErrorBody(decision.Reason, requested),
	})
}

func isPerCallSourceFormat(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "openai-image", "openai-video":
		return true
	default:
		return false
	}
}

func policyErrorBody(reason, model string) []byte {
	message := "request rejected by key policy"
	switch reason {
	case "model_required":
		message = "model is required"
	case "model_not_allowed":
		message = "model is not allowed for this key: " + model
	case "rpm_exceeded":
		message = "request rate limit exceeded"
	case "daily_exceeded":
		message = "daily budget exceeded"
	case "weekly_exceeded":
		message = "weekly budget exceeded"
	case "key_disabled":
		message = "key is disabled"
	}
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "key_policy_error",
			"code":    reason,
		},
	})
	return body
}

func (a *App) handleUsage(raw []byte) ([]byte, error) {
	var req UsageHandleRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return OKEnvelope(UsageHandleResponse{})
	}
	_ = a.store.RecordUsage(req.APIKey, req.Alias, req.Model, req.Failed, policy.UsageDetail{
		Provider:            req.Provider,
		InputTokens:         req.Detail.InputTokens,
		OutputTokens:        req.Detail.OutputTokens,
		ReasoningTokens:     req.Detail.ReasoningTokens,
		CachedTokens:        req.Detail.CachedTokens,
		CacheReadTokens:     req.Detail.CacheReadTokens,
		CacheCreationTokens: req.Detail.CacheCreationTokens,
		TotalTokens:         req.Detail.TotalTokens,
	})
	return OKEnvelope(UsageHandleResponse{})
}

func (a *App) managementRegistration() ManagementRegistrationResponse {
	base := "/plugins/" + PluginID
	return ManagementRegistrationResponse{
		Routes: []ManagementRoute{
			{Method: http.MethodGet, Path: base + "/keys", Description: "List downstream CPA key policies."},
			{Method: http.MethodPost, Path: base + "/keys", Description: "Create a downstream CPA key policy."},
			{Method: http.MethodPatch, Path: base + "/keys", Description: "Update a downstream CPA key policy by id."},
			{Method: http.MethodDelete, Path: base + "/keys", Description: "Delete a downstream CPA key policy by id."},
			{Method: http.MethodPost, Path: base + "/keys/rotate", Description: "Rotate one downstream CPA key by id."},
			{Method: http.MethodPost, Path: base + "/keys/reset-rpm", Description: "Reset one downstream CPA key RPM counter by id."},
			{Method: http.MethodPost, Path: base + "/keys/reset-usage", Description: "Reset daily and weekly usage for all downstream CPA keys."},
			{Method: http.MethodGet, Path: base + "/keys/usage", Description: "Per-model usage breakdown for one downstream CPA key by id."},
			{Method: http.MethodGet, Path: base + "/status", Description: "Show cpa-key-policy runtime status."},
		},
		Resources: []ResourceRoute{
			{Path: web.IndexPath, Menu: "Key Policy", Description: "Web UI for managing downstream CPA key policies."},
		},
	}
}

func (a *App) handleManagement(raw []byte) ([]byte, error) {
	var req ManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	path := strings.TrimRight(req.Path, "/")
	resourcePrefix := "/v0/resource/plugins/" + PluginID
	if req.Method == http.MethodGet && strings.HasPrefix(path, resourcePrefix) {
		status, headers, body := web.Serve(strings.TrimPrefix(path, resourcePrefix))
		return OKEnvelope(ManagementResponse{StatusCode: status, Headers: headers, Body: body})
	}

	base := "/v0/management/plugins/" + PluginID
	switch {
	case req.Method == http.MethodGet && path == base+"/keys":
		return OKEnvelope(jsonResponse(http.StatusOK, map[string]any{"keys": a.publicKeys(a.store.Keys())}))
	case req.Method == http.MethodPost && path == base+"/keys":
		return OKEnvelope(a.createKey(req.Body))
	case req.Method == http.MethodPatch && path == base+"/keys":
		return OKEnvelope(a.patchKey(req.Body))
	case req.Method == http.MethodDelete && path == base+"/keys":
		return OKEnvelope(a.deleteKey(idFromRequest(req.Query, req.Body)))
	case req.Method == http.MethodPost && path == base+"/keys/rotate":
		return OKEnvelope(a.rotateKey(idFromRequest(req.Query, req.Body)))
	case req.Method == http.MethodPost && path == base+"/keys/reset-rpm":
		return OKEnvelope(a.resetRPM(idFromRequest(req.Query, req.Body)))
	case req.Method == http.MethodPost && path == base+"/keys/reset-usage":
		return OKEnvelope(a.resetUsage())
	case req.Method == http.MethodGet && path == base+"/keys/usage":
		return OKEnvelope(a.keyUsage(idFromRequest(req.Query, req.Body)))
	case req.Method == http.MethodGet && path == base+"/status":
		return OKEnvelope(jsonResponse(http.StatusOK, a.store.Status()))
	default:
		return OKEnvelope(jsonError(http.StatusNotFound, "not_found", "unknown management route"))
	}
}

type keyWriteRequest struct {
	ID                  string             `json:"id"`
	Name                *string            `json:"name,omitempty"`
	Enabled             *bool              `json:"enabled,omitempty"`
	Key                 string             `json:"key,omitempty"`
	RPM                 *int               `json:"rpm,omitempty"`
	Models              []policy.ModelRule `json:"models,omitempty"`
	DailyLimitUSD       *float64           `json:"daily_limit_usd,omitempty"`
	WeeklyLimitUSD      *float64           `json:"weekly_limit_usd,omitempty"`
	AllowModelsEndpoint *bool              `json:"allow_models_endpoint,omitempty"`
}

type publicKey struct {
	ID                  string              `json:"id"`
	Name                string              `json:"name"`
	Enabled             bool                `json:"enabled"`
	KeyPreview          string              `json:"key_preview"`
	RPM                 int                 `json:"rpm"`
	Models              []policy.ModelRule  `json:"models"`
	DailyLimitUSD       float64             `json:"daily_limit_usd"`
	WeeklyLimitUSD      float64             `json:"weekly_limit_usd"`
	AllowModelsEndpoint bool                `json:"allow_models_endpoint,omitempty"`
	Usage               policy.UsageSummary `json:"usage"`
	CreatedAt           string              `json:"created_at,omitempty"`
	UpdatedAt           string              `json:"updated_at,omitempty"`
}

func (a *App) createKey(body []byte) ManagementResponse {
	var req keyWriteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		return jsonError(http.StatusBadRequest, "missing_id", "id is required")
	}
	plain := strings.TrimSpace(req.Key)
	generated := false
	var err error
	if plain == "" {
		plain, err = policy.GenerateKey()
		if err != nil {
			return jsonError(http.StatusInternalServerError, "key_generation_failed", err.Error())
		}
		generated = true
	}
	hash, err := policy.HashKey(plain)
	if err != nil {
		return jsonError(http.StatusBadRequest, "invalid_key", err.Error())
	}
	item := policy.KeyConfig{
		ID:                  req.ID,
		Name:                stringValue(req.Name, req.ID),
		Enabled:             boolValue(req.Enabled, true),
		KeyHash:             hash,
		KeyPreview:          policy.PreviewKey(plain),
		RPM:                 intValue(req.RPM, 0),
		Models:              req.Models,
		DailyLimitUSD:       floatValue(req.DailyLimitUSD, 0),
		WeeklyLimitUSD:      floatValue(req.WeeklyLimitUSD, 0),
		AllowModelsEndpoint: boolValue(req.AllowModelsEndpoint, false),
	}
	if err := a.store.UpsertKey(item, true); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_policy", err.Error())
	}
	stored, _ := a.keyByID(req.ID)
	return jsonResponse(http.StatusCreated, map[string]any{
		"key":       a.publicKeyFromConfig(stored),
		"plain_key": plain,
		"generated": generated,
	})
}

func (a *App) patchKey(body []byte) ManagementResponse {
	var req keyWriteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	id := strings.TrimSpace(req.ID)
	current, ok := a.keyByID(id)
	if id == "" {
		return jsonError(http.StatusBadRequest, "missing_id", "id is required")
	}
	if !ok {
		return jsonError(http.StatusNotFound, "not_found", "key not found")
	}
	if req.Name != nil {
		current.Name = strings.TrimSpace(*req.Name)
	}
	if req.Enabled != nil {
		current.Enabled = *req.Enabled
	}
	if req.RPM != nil {
		current.RPM = *req.RPM
	}
	if req.Models != nil {
		current.Models = req.Models
	}
	if req.DailyLimitUSD != nil {
		current.DailyLimitUSD = *req.DailyLimitUSD
	}
	if req.WeeklyLimitUSD != nil {
		current.WeeklyLimitUSD = *req.WeeklyLimitUSD
	}
	if req.AllowModelsEndpoint != nil {
		current.AllowModelsEndpoint = *req.AllowModelsEndpoint
	}
	if strings.TrimSpace(req.Key) != "" {
		hash, err := policy.HashKey(req.Key)
		if err != nil {
			return jsonError(http.StatusBadRequest, "invalid_key", err.Error())
		}
		current.KeyHash = hash
		current.KeyPreview = policy.PreviewKey(req.Key)
	}
	if err := a.store.UpsertKey(current, true); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_policy", err.Error())
	}
	stored, _ := a.keyByID(id)
	return jsonResponse(http.StatusOK, map[string]any{"key": a.publicKeyFromConfig(stored)})
}

func (a *App) keyByID(id string) (policy.KeyConfig, bool) {
	for _, key := range a.store.Keys() {
		if key.ID == id {
			return key, true
		}
	}
	return policy.KeyConfig{}, false
}

func (a *App) deleteKey(id string) ManagementResponse {
	if err := a.store.DeleteKey(id); err != nil {
		return storeError(err)
	}
	return jsonResponse(http.StatusOK, map[string]any{"deleted": true, "id": strings.TrimSpace(id)})
}

func (a *App) rotateKey(id string) ManagementResponse {
	plain, item, err := a.store.RotateKey(id)
	if err != nil {
		return storeError(err)
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"key":       a.publicKeyFromConfig(item),
		"plain_key": plain,
		"generated": true,
	})
}

func (a *App) resetRPM(id string) ManagementResponse {
	if err := a.store.ResetRPM(id); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"reset": true, "id": strings.TrimSpace(id)})
}

func (a *App) resetUsage() ManagementResponse {
	if err := a.store.ResetAllUsage(); err != nil {
		return jsonError(http.StatusInternalServerError, "persist_failed", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"reset": true, "scope": "all"})
}

func (a *App) keyUsage(id string) ManagementResponse {
	id = strings.TrimSpace(id)
	if id == "" {
		return jsonError(http.StatusBadRequest, "missing_id", "id is required")
	}
	key, models, ok := a.store.ModelUsageFor(id)
	if !ok {
		return jsonError(http.StatusNotFound, "not_found", "key not found")
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"key_id":           key.ID,
		"key_name":         key.Name,
		"daily_limit_usd":  key.DailyLimitUSD,
		"weekly_limit_usd": key.WeeklyLimitUSD,
		"models":           models,
	})
}

func storeError(err error) ManagementResponse {
	if errors.Is(err, policy.ErrUnknownKey) {
		return jsonError(http.StatusNotFound, "not_found", "key not found")
	}
	return jsonError(http.StatusBadRequest, "invalid_request", err.Error())
}

func idFromRequest(query map[string][]string, body []byte) string {
	if query != nil {
		for _, name := range []string{"id", "key_id"} {
			if values := query[name]; len(values) > 0 && strings.TrimSpace(values[0]) != "" {
				return strings.TrimSpace(values[0])
			}
		}
	}
	var payload struct {
		ID    string `json:"id"`
		KeyID string `json:"key_id"`
	}
	if len(body) > 0 && json.Unmarshal(body, &payload) == nil {
		if strings.TrimSpace(payload.ID) != "" {
			return strings.TrimSpace(payload.ID)
		}
		return strings.TrimSpace(payload.KeyID)
	}
	return ""
}

func (a *App) publicKeys(keys []policy.KeyConfig) []publicKey {
	out := make([]publicKey, 0, len(keys))
	for _, key := range keys {
		out = append(out, a.publicKeyFromConfig(key))
	}
	return out
}

func (a *App) publicKeyFromConfig(key policy.KeyConfig) publicKey {
	out := publicKey{
		ID:                  key.ID,
		Name:                key.Name,
		Enabled:             key.Enabled,
		KeyPreview:          key.KeyPreview,
		RPM:                 key.RPM,
		Models:              append([]policy.ModelRule{}, key.Models...),
		DailyLimitUSD:       key.DailyLimitUSD,
		WeeklyLimitUSD:      key.WeeklyLimitUSD,
		AllowModelsEndpoint: key.AllowModelsEndpoint,
		Usage:               a.store.UsageSummaryFor(key),
	}
	if !key.CreatedAt.IsZero() {
		out.CreatedAt = key.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if !key.UpdatedAt.IsZero() {
		out.UpdatedAt = key.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return out
}

func stringValue(value *string, fallback string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fallback
	}
	return strings.TrimSpace(*value)
}

func boolValue(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func intValue(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func floatValue(value *float64, fallback float64) float64 {
	if value == nil {
		return fallback
	}
	return *value
}

func jsonResponse(status int, payload any) ManagementResponse {
	body, err := json.Marshal(payload)
	if err != nil {
		return jsonError(http.StatusInternalServerError, "json_error", err.Error())
	}
	return ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	}
}

func jsonError(status int, code, message string) ManagementResponse {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": message}})
	return ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	}
}

func (a *App) Store() *policy.Store {
	return a.store
}

func DebugEnvelope(raw []byte) string {
	var env Envelope
	if json.Unmarshal(raw, &env) != nil {
		return "invalid"
	}
	if env.OK {
		return "ok"
	}
	if env.Error != nil {
		return env.Error.Code
	}
	return "error"
}

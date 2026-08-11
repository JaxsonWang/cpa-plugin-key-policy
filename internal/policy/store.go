package policy

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type Store struct {
	mu         sync.RWMutex
	updateMu   sync.Mutex
	persistMu  sync.Mutex
	enabled    bool
	statePath  string
	keys       map[string]*KeyConfig
	keysByHash map[string]*KeyConfig
	limiter    *RateLimiter
	usage      *usageLedger
	flusher    *usageFlusher
}

type AuthDecision struct {
	Known       bool
	Allowed     bool
	KeyID       string
	Principal   string
	Requested   string
	Rule        ModelRule
	Reason      string
	ModelList   bool
	RateLimited bool
	CostLimited bool
	PreCharged  bool
}

func NewStore() *Store {
	return &Store{
		enabled:    DefaultConfig().Enabled,
		keys:       make(map[string]*KeyConfig),
		keysByHash: make(map[string]*KeyConfig),
		limiter:    NewRateLimiter(),
		usage:      newUsageLedger(time.Now),
	}
}

func (s *Store) SetClock(now func() time.Time) {
	if now == nil {
		return
	}
	s.mu.Lock()
	s.limiter = NewRateLimiterWithClock(now)
	s.usage = newUsageLedger(now)
	s.mu.Unlock()
}

func (s *Store) Configure(cfg Config) error {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	if err := normalizeConfig(&cfg); err != nil {
		return err
	}
	statePath, err := ResolveStatePath(cfg.StateFile)
	if err != nil {
		return err
	}

	// Persist usage to the previous state file before replacing the in-memory
	// store. This keeps reconfigure from losing the latest accounting window.
	s.StopUsageFlusher()

	keys := cfg.Keys
	var loadedUsage map[string]*UsageState
	firstBoot := false
	migratedState := false
	if state, loadErr := LoadState(statePath); loadErr == nil {
		migratedState = stateNeedsModelMigration(state)
		merged := Config{
			Enabled:   cfg.Enabled,
			StateFile: cfg.StateFile,
			Keys:      state.Keys,
			Aliases:   state.Aliases,
		}
		if len(merged.Aliases) == 0 {
			merged.Aliases = cfg.Aliases
		}
		if err := normalizeConfig(&merged); err != nil {
			return fmt.Errorf("load state: %w", err)
		}
		keys = merged.Keys
		loadedUsage = state.Usage
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		return fmt.Errorf("load state: %w", loadErr)
	} else {
		firstBoot = true
	}

	next := make(map[string]*KeyConfig, len(keys))
	now := time.Now().UTC()
	for i := range keys {
		item := keys[i]
		if item.CreatedAt.IsZero() {
			item.CreatedAt = now
		}
		if item.UpdatedAt.IsZero() {
			item.UpdatedAt = item.CreatedAt
		}
		next[item.ID] = &item
	}

	s.mu.Lock()
	if s.flusher != nil {
		s.flusher.stop()
		s.flusher = nil
	}
	s.enabled = cfg.Enabled
	s.statePath = statePath
	s.keys = next
	s.rebuildKeysByHashLocked()
	if s.limiter == nil {
		s.limiter = NewRateLimiter()
	}
	clockNow := time.Now
	if s.usage != nil && s.usage.now != nil {
		clockNow = s.usage.now
	}
	s.usage = newUsageLedger(clockNow)
	s.usage.loadFromState(loadedUsage)
	baseKeys := s.keysSnapshotLocked()
	baseUsage := s.usageSnapshotLocked()
	s.mu.Unlock()

	if firstBoot || migratedState {
		if err := s.saveState(statePath, baseKeys, baseUsage); err != nil {
			if migratedState {
				return fmt.Errorf("persist migrated state: %w", err)
			}
			return fmt.Errorf("seed state: %w", err)
		}
	}
	return nil
}

func stateNeedsModelMigration(state *State) bool {
	if state == nil {
		return false
	}
	if state.Version < 2 || len(state.Aliases) > 0 {
		return true
	}
	for _, key := range state.Keys {
		if len(key.Aliases) > 0 {
			return true
		}
		for _, rule := range key.Models {
			if strings.TrimSpace(rule.Model) == "" ||
				strings.TrimSpace(rule.Alias) != "" ||
				strings.TrimSpace(rule.Provider) != "" ||
				strings.TrimSpace(rule.TargetModel) != "" ||
				strings.TrimSpace(rule.Group) != "" {
				return true
			}
			if normalizePrice(rule.InputPricePerMillion) != rule.InputPricePerMillion ||
				normalizePrice(rule.OutputPricePerMillion) != rule.OutputPricePerMillion ||
				normalizePrice(rule.CacheReadPricePerMillion) != rule.CacheReadPricePerMillion ||
				normalizePrice(rule.PerCallUSD) != rule.PerCallUSD {
				return true
			}
		}
	}
	return false
}

func (s *Store) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled
}

func (s *Store) runtimeComponents() (*RateLimiter, *usageLedger) {
	s.mu.RLock()
	limiter := s.limiter
	usage := s.usage
	s.mu.RUnlock()
	return limiter, usage
}

func (s *Store) StatePath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.statePath
}

// AuthenticateWebSocket validates the downstream key on the HTTP Upgrade.
// The upgrade carries no model request, so RPM, budget and model checks are
// deliberately deferred to AuthorizeModel for each WebSocket execution frame.
func (s *Store) AuthenticateWebSocket(headers http.Header, query map[string][]string) AuthDecision {
	key, enabled := s.findBySecretWhenEnabled(ExtractAPIKey(headers, query))
	if !enabled {
		return AuthDecision{Reason: "plugin_disabled"}
	}
	if key == nil {
		return AuthDecision{Reason: "unknown_key"}
	}
	decision := AuthDecision{Known: true, KeyID: key.ID, Principal: key.ID}
	if !key.Enabled {
		decision.Reason = "key_disabled"
		return decision
	}
	decision.Allowed = true
	decision.Reason = "websocket_authenticated"
	return decision
}

func (s *Store) Authenticate(method, path string, headers http.Header, query map[string][]string, body []byte) AuthDecision {
	requested := ExtractRequestedModel(path, query, body)
	decision := s.authorize(headers, query, requested, IsModelsEndpoint(path), false)
	if !decision.Allowed || decision.ModelList {
		return decision
	}
	if decision.Rule.BillingMode == "per_call" && IsImageVideoEndpoint(path) {
		model := decision.Rule.Model
		if model == "" {
			model = decision.Requested
		}
		s.RecordUsage(decision.KeyID, model, model, false, UsageDetail{})
		decision.PreCharged = true
	}
	return decision
}

// AuthorizeModel applies the per-execution policy used by WebSocket request
// interception. It is also safe for a request whose body omitted model because
// CPA supplies the resolved requested model separately.
func (s *Store) AuthorizeModel(headers http.Header, query map[string][]string, requested string) AuthDecision {
	return s.authorize(headers, query, strings.TrimSpace(requested), false, true)
}

func (s *Store) authorize(headers http.Header, query map[string][]string, requested string, modelList, requireModel bool) AuthDecision {
	rawKey := ExtractAPIKey(headers, query)
	key, enabled := s.findBySecretWhenEnabled(rawKey)
	if !enabled {
		return AuthDecision{Reason: "plugin_disabled"}
	}
	if key == nil {
		return AuthDecision{Reason: "unknown_key"}
	}
	decision := AuthDecision{
		Known:     true,
		KeyID:     key.ID,
		Principal: key.ID,
		Requested: strings.TrimSpace(requested),
		ModelList: modelList,
	}
	if !key.Enabled {
		decision.Reason = "key_disabled"
		return decision
	}
	if modelList {
		if key.AllowModelsEndpoint {
			decision.Allowed = true
			decision.Reason = "models_endpoint_allowed"
		} else {
			decision.Reason = "models_endpoint_disabled"
		}
		return decision
	}
	if decision.Requested == "" {
		if requireModel {
			decision.Reason = "model_required"
			return decision
		}
	} else {
		rule, ok := key.ModelRuleForModel(decision.Requested)
		if !ok {
			decision.Reason = "model_not_allowed"
			return decision
		}
		decision.Rule = rule
	}
	limiter, usage := s.runtimeComponents()
	if limiter != nil && !limiter.Allow(key.ID, key.RPM) {
		decision.RateLimited = true
		decision.Reason = "rpm_exceeded"
		return decision
	}
	if usage != nil {
		if reason, _ := usage.OverLimit(*key); reason != "" {
			decision.CostLimited = true
			decision.Reason = reason
			return decision
		}
	}
	decision.Allowed = true
	decision.Reason = "allowed"
	return decision
}

// RecordResponseCost is retained for non-streaming unit/integration callers.
// Production accounting is centralized in usage.handle.
func (s *Store) RecordResponseCost(headers http.Header, query map[string][]string, requested string, body []byte) float64 {
	if !s.Enabled() {
		return 0
	}
	key := s.findBySecret(ExtractAPIKey(headers, query))
	if key == nil || !key.Enabled {
		return 0
	}
	model := strings.TrimSpace(requested)
	if model == "" {
		return 0
	}
	usage := ParseTokenUsage(body)
	if !usage.Found {
		return 0
	}
	inputPrice, outputPrice, _, priced := key.PriceForModel(model)
	cost := ComputeCost(inputPrice, outputPrice, priced, usage)
	_, ledger := s.runtimeComponents()
	if priced && ledger != nil {
		ledger.RecordCost(key.ID, model, cost, 0, 0, int64(usage.PromptTokens), int64(usage.CompletionTokens), 1)
	}
	return cost
}

func (s *Store) RecordUsage(apiKeyOrID, requestedModel, model string, failed bool, detail UsageDetail) float64 {
	if !s.Enabled() {
		return 0
	}
	key := s.findByID(apiKeyOrID)
	if key == nil || !key.Enabled {
		key = s.findBySecret(apiKeyOrID)
	}
	if key == nil || !key.Enabled {
		return 0
	}
	resolved := strings.TrimSpace(requestedModel)
	if resolved == "" {
		resolved = strings.TrimSpace(model)
	}
	if resolved == "" {
		return 0
	}
	rule, ok := key.ModelRuleForModel(resolved)
	if !ok {
		return 0
	}
	_, ledger := s.runtimeComponents()
	if rule.BillingMode == "per_call" {
		if failed {
			return 0
		}
		cost := rule.PerCallUSD
		if ledger != nil {
			ledger.RecordCost(key.ID, resolved, cost, 0, 0, 0, 0, 1)
		}
		return cost
	}
	usage := TokenUsage{
		PromptTokens:     int(detail.InputTokens),
		CompletionTokens: int(detail.OutputTokens),
		Found:            detail.InputTokens > 0 || detail.OutputTokens > 0,
	}
	if !usage.Found {
		return 0
	}
	inputPrice, outputPrice, cachePrice, priced := key.PriceForModel(resolved)
	cost, cacheCost, cacheRead := ComputeCacheCostBreakdown(detail.Provider, inputPrice, outputPrice, cachePrice, priced, detail)
	if !priced || ledger == nil {
		return cost
	}
	inputTokens := nonCacheInputTokens(detail.Provider, detail, cacheRead)
	ledger.RecordCost(key.ID, resolved, cost, cacheCost, cacheRead, inputTokens, detail.OutputTokens, 1)
	return cost
}

func nonCacheInputTokens(provider string, detail UsageDetail, cacheRead int64) int64 {
	input := detail.InputTokens
	if isCacheAdditiveProvider(provider) {
		input += detail.CacheCreationTokens
		if input < 0 {
			return 0
		}
		return input
	}
	if cacheRead > input {
		return 0
	}
	return input - cacheRead
}

func (s *Store) UsageSummaryFor(key KeyConfig) UsageSummary {
	_, usage := s.runtimeComponents()
	if usage == nil {
		return UsageSummary{DailyLimitUSD: key.DailyLimitUSD, WeeklyLimitUSD: key.WeeklyLimitUSD}
	}
	return usage.Summary(key)
}

func (s *Store) ModelUsageFor(keyID string) (KeyConfig, []ModelUsageEntry, bool) {
	key := s.findByID(keyID)
	if key == nil {
		return KeyConfig{}, nil, false
	}
	_, usage := s.runtimeComponents()
	if usage == nil {
		rows := make([]ModelUsageEntry, 0, len(key.Models))
		for _, rule := range key.Models {
			rows = append(rows, ModelUsageEntry{
				Model:       rule.Model,
				BillingMode: rule.BillingMode,
				PerCallUSD:  rule.PerCallUSD,
				InConfig:    true,
			})
		}
		return *key, rows, true
	}
	return *key, usage.ModelUsage(*key), true
}

func (s *Store) FindByAPIKey(raw string) *KeyConfig {
	return s.findBySecret(raw)
}

func (s *Store) findBySecret(raw string) *KeyConfig {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	hash, err := HashKey(raw)
	if err != nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copyKey(s.keysByHash[strings.ToLower(strings.TrimSpace(hash))])
}

func (s *Store) findBySecretWhenEnabled(raw string) (*KeyConfig, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		s.mu.RLock()
		enabled := s.enabled
		s.mu.RUnlock()
		return nil, enabled
	}
	hash, err := HashKey(raw)
	if err != nil {
		return nil, true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.enabled {
		return nil, false
	}
	return copyKey(s.keysByHash[strings.ToLower(strings.TrimSpace(hash))]), true
}

func (s *Store) findByID(id string) *KeyConfig {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := s.keys[id]
	if key == nil {
		for candidateID, candidate := range s.keys {
			if strings.EqualFold(candidateID, id) {
				key = candidate
				break
			}
		}
	}
	return copyKey(key)
}

func copyKey(key *KeyConfig) *KeyConfig {
	if key == nil {
		return nil
	}
	copy := *key
	copy.Models = append([]ModelRule(nil), key.Models...)
	copy.Aliases = nil
	return &copy
}

func (s *Store) rebuildKeysByHashLocked() {
	ids := make([]string, 0, len(s.keys))
	for id := range s.keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	byHash := make(map[string]*KeyConfig, len(ids))
	for _, id := range ids {
		key := s.keys[id]
		if key == nil {
			continue
		}
		hash := strings.ToLower(strings.TrimSpace(key.KeyHash))
		if hash == "" {
			continue
		}
		if _, exists := byHash[hash]; !exists {
			byHash[hash] = key
		}
	}
	s.keysByHash = byHash
}

func (k *KeyConfig) ModelRuleForModel(model string) (ModelRule, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return ModelRule{}, false
	}
	for _, rule := range k.Models {
		if strings.EqualFold(strings.TrimSpace(rule.Model), model) {
			return rule, true
		}
	}
	return ModelRule{}, false
}

func (s *Store) Keys() []KeyConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.keysSnapshotLocked()
}

func (s *Store) keysSnapshotLocked() []KeyConfig {
	keys := make([]KeyConfig, 0, len(s.keys))
	for _, key := range s.keys {
		if key == nil {
			continue
		}
		keys = append(keys, *copyKey(key))
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].ID < keys[j].ID })
	return keys
}

func (s *Store) UpsertKey(input KeyConfig, persist bool) error {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	cfg := Config{Enabled: true, StateFile: s.StatePath(), Keys: []KeyConfig{input}}
	if err := normalizeConfig(&cfg); err != nil {
		return err
	}
	key := cfg.Keys[0]
	now := time.Now().UTC()
	s.mu.Lock()
	if old := s.keys[key.ID]; old != nil && !old.CreatedAt.IsZero() {
		key.CreatedAt = old.CreatedAt
	} else if key.CreatedAt.IsZero() {
		key.CreatedAt = now
	}
	key.UpdatedAt = now
	s.keys[key.ID] = &key
	s.rebuildKeysByHashLocked()
	keys := s.keysSnapshotLocked()
	path := s.statePath
	usage := s.usageSnapshotLocked()
	s.mu.Unlock()
	if persist {
		return s.saveState(path, keys, usage)
	}
	return nil
}

func (s *Store) DeleteKey(id string) error {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("id is required")
	}
	s.mu.Lock()
	if _, ok := s.keys[id]; !ok {
		s.mu.Unlock()
		return ErrUnknownKey
	}
	delete(s.keys, id)
	s.rebuildKeysByHashLocked()
	keys := s.keysSnapshotLocked()
	usage := s.usageSnapshotLocked()
	path := s.statePath
	limiter := s.limiter
	ledger := s.usage
	s.mu.Unlock()
	if limiter != nil {
		limiter.Reset(id)
	}
	if ledger != nil {
		ledger.resetUsage(id)
	}
	return s.saveState(path, keys, usage)
}

func (s *Store) RotateKey(id string) (string, KeyConfig, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	id = strings.TrimSpace(id)
	if id == "" {
		return "", KeyConfig{}, errors.New("id is required")
	}
	plain, err := GenerateKey()
	if err != nil {
		return "", KeyConfig{}, err
	}
	hash, err := HashKey(plain)
	if err != nil {
		return "", KeyConfig{}, err
	}
	s.mu.Lock()
	key := s.keys[id]
	if key == nil {
		s.mu.Unlock()
		return "", KeyConfig{}, ErrUnknownKey
	}
	key.KeyHash = hash
	key.KeyPreview = PreviewKey(plain)
	key.UpdatedAt = time.Now().UTC()
	result := *copyKey(key)
	s.rebuildKeysByHashLocked()
	keys := s.keysSnapshotLocked()
	usage := s.usageSnapshotLocked()
	path := s.statePath
	limiter := s.limiter
	s.mu.Unlock()
	if limiter != nil {
		limiter.Reset(id)
	}
	if err := s.saveState(path, keys, usage); err != nil {
		return "", KeyConfig{}, err
	}
	return plain, result, nil
}

func (s *Store) ResetRPM(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("id is required")
	}
	limiter, _ := s.runtimeComponents()
	if limiter != nil {
		limiter.Reset(id)
	}
	return nil
}

func (s *Store) usageSnapshotLocked() map[string]*UsageState {
	if s.usage == nil {
		return make(map[string]*UsageState)
	}
	return s.usage.snapshot()
}

func (s *Store) FlushUsage() error {
	// Serialize the snapshot with persistence. Taking the snapshot before this
	// lock could let an older background flush overwrite a manual usage reset.
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	s.mu.RLock()
	path := s.statePath
	usage := s.usageSnapshotLocked()
	s.mu.RUnlock()
	if strings.TrimSpace(path) == "" {
		return nil
	}
	return SaveUsageOnly(path, usage)
}

// ResetAllUsage clears every key's daily and weekly usage and persists the
// reset before changing the in-memory ledger. A persistence failure therefore
// leaves the live counters untouched instead of making disk and memory diverge.
func (s *Store) ResetAllUsage() error {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	s.mu.RLock()
	path := s.statePath
	usage := s.usage
	s.mu.RUnlock()
	if strings.TrimSpace(path) != "" {
		if err := SaveUsageOnly(path, make(map[string]*UsageState)); err != nil {
			return err
		}
	}
	if usage != nil {
		usage.resetAllUsage()
	}
	return nil
}

func (s *Store) saveState(path string, keys []KeyConfig, usage map[string]*UsageState) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	return SaveState(path, keys, usage)
}

type usageFlusher struct {
	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
	store    *Store
}

func (s *Store) StartUsageFlusher() func() {
	s.mu.Lock()
	if s.flusher != nil {
		flusher := s.flusher
		s.mu.Unlock()
		return flusher.stop
	}
	flusher := &usageFlusher{stopCh: make(chan struct{}), doneCh: make(chan struct{}), store: s}
	s.flusher = flusher
	s.mu.Unlock()
	go flusher.loop()
	return flusher.stop
}

func (s *Store) StopUsageFlusher() {
	s.mu.Lock()
	flusher := s.flusher
	s.flusher = nil
	s.mu.Unlock()
	if flusher != nil {
		flusher.stop()
		<-flusher.doneCh
	}
	_ = s.FlushUsage()
}

func (f *usageFlusher) stop() {
	f.stopOnce.Do(func() { close(f.stopCh) })
}

func (f *usageFlusher) loop() {
	defer close(f.doneCh)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = f.store.FlushUsage()
		case <-f.stopCh:
			return
		}
	}
}

func (s *Store) Status() map[string]any {
	keys := s.Keys()
	limiter, usage := s.runtimeComponents()
	status := map[string]any{
		"enabled":    s.Enabled(),
		"state_file": s.StatePath(),
		"key_count":  len(keys),
	}
	if limiter != nil {
		status["rpm_usage"] = limiter.Snapshot()
	}
	if usage != nil {
		status["usage"] = usageSummaryForKeys(usage, keys)
	}
	return status
}

func usageSummaryForKeys(usage *usageLedger, keys []KeyConfig) map[string]UsageSummary {
	out := make(map[string]UsageSummary, len(keys))
	for _, key := range keys {
		out[key.ID] = usage.Summary(key)
	}
	return out
}

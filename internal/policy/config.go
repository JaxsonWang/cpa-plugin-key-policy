package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Enabled   bool        `yaml:"enabled" json:"enabled"`
	StateFile string      `yaml:"state_file" json:"state_file"`
	Keys      []KeyConfig `yaml:"keys" json:"keys"`

	// Aliases is read only to migrate state/config written by v0.4.x. Direct
	// model policies are canonical from v0.5.0 onward, so normalization clears
	// this table before the configuration enters the runtime store.
	Aliases []AliasMapping `yaml:"aliases,omitempty" json:"aliases,omitempty"`
}

const priceDecimalScale = 1_000_000_000_000

func normalizePrice(value float64) float64 {
	if math.Abs(value) > math.MaxFloat64/priceDecimalScale {
		return value
	}
	return math.Round(value*priceDecimalScale) / priceDecimalScale
}

type KeyConfig struct {
	ID                  string      `yaml:"id" json:"id"`
	Name                string      `yaml:"name" json:"name"`
	Enabled             bool        `yaml:"enabled" json:"enabled"`
	KeyHash             string      `yaml:"key_hash" json:"key_hash"`
	KeyPreview          string      `yaml:"key_preview" json:"key_preview"`
	RPM                 int         `yaml:"rpm" json:"rpm"`
	Models              []ModelRule `yaml:"models" json:"models"`
	AllowModelsEndpoint bool        `yaml:"allow_models_endpoint,omitempty" json:"allow_models_endpoint,omitempty"`
	DailyLimitUSD       float64     `yaml:"daily_limit_usd,omitempty" json:"daily_limit_usd,omitempty"`
	WeeklyLimitUSD      float64     `yaml:"weekly_limit_usd,omitempty" json:"weekly_limit_usd,omitempty"`
	CreatedAt           time.Time   `yaml:"created_at,omitempty" json:"created_at,omitempty"`
	UpdatedAt           time.Time   `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`

	// Aliases is a v0.4.x migration input. It is never persisted by v0.5.0.
	Aliases []KeyAliasRef `yaml:"aliases,omitempty" json:"aliases,omitempty"`
}

// ModelRule is a direct allow-list entry. Model is the exact model name the
// client sends and CPA resolves natively. The legacy routing fields are kept
// only so v0.4.x config/state can be migrated without losing keys or pricing.
type ModelRule struct {
	Model                    string  `yaml:"model,omitempty" json:"model"`
	InputPricePerMillion     float64 `yaml:"input_price_per_million,omitempty" json:"input_price_per_million,omitempty"`
	OutputPricePerMillion    float64 `yaml:"output_price_per_million,omitempty" json:"output_price_per_million,omitempty"`
	CacheReadPricePerMillion float64 `yaml:"cache_read_price_per_million,omitempty" json:"cache_read_price_per_million,omitempty"`
	BillingMode              string  `yaml:"billing_mode,omitempty" json:"billing_mode,omitempty"`
	PerCallUSD               float64 `yaml:"per_call_usd,omitempty" json:"per_call_usd,omitempty"`
	Alias                    string  `yaml:"alias,omitempty" json:"alias,omitempty"`
	Provider                 string  `yaml:"provider,omitempty" json:"provider,omitempty"`
	TargetModel              string  `yaml:"target_model,omitempty" json:"target_model,omitempty"`
	Group                    string  `yaml:"group,omitempty" json:"group,omitempty"`
}

// The following three types describe the removed v0.4.x alias/router schema.
// They remain decode-only so an upgrade can turn every old target into an
// exact direct model entry while preserving price overrides.
type AliasMapping struct {
	Alias                    string        `yaml:"alias" json:"alias"`
	Targets                  []AliasTarget `yaml:"targets" json:"targets"`
	Dispatch                 string        `yaml:"dispatch,omitempty" json:"dispatch,omitempty"`
	BillingMode              string        `yaml:"billing_mode,omitempty" json:"billing_mode,omitempty"`
	InputPricePerMillion     float64       `yaml:"input_price_per_million,omitempty" json:"input_price_per_million,omitempty"`
	OutputPricePerMillion    float64       `yaml:"output_price_per_million,omitempty" json:"output_price_per_million,omitempty"`
	CacheReadPricePerMillion float64       `yaml:"cache_read_price_per_million,omitempty" json:"cache_read_price_per_million,omitempty"`
	PerCallUSD               float64       `yaml:"per_call_usd,omitempty" json:"per_call_usd,omitempty"`
}

type AliasTarget struct {
	Provider    string `yaml:"provider" json:"provider"`
	TargetModel string `yaml:"target_model" json:"target_model"`
	Group       string `yaml:"group,omitempty" json:"group,omitempty"`
}

type KeyAliasRef struct {
	Alias                    string   `yaml:"alias" json:"alias"`
	InputPricePerMillion     *float64 `yaml:"input_price_per_million,omitempty" json:"input_price_per_million,omitempty"`
	OutputPricePerMillion    *float64 `yaml:"output_price_per_million,omitempty" json:"output_price_per_million,omitempty"`
	CacheReadPricePerMillion *float64 `yaml:"cache_read_price_per_million,omitempty" json:"cache_read_price_per_million,omitempty"`
	PerCallUSD               *float64 `yaml:"per_call_usd,omitempty" json:"per_call_usd,omitempty"`
}

type UsageState struct {
	Daily   UsageWindow                  `json:"daily"`
	Weekly  UsageWindow                  `json:"weekly"`
	ByAlias map[string]AliasUsageWindows `json:"by_alias,omitempty"`
}

type AliasUsageWindows struct {
	Daily  UsageWindow `json:"daily"`
	Weekly UsageWindow `json:"weekly"`
}

// UnmarshalJSON keeps the v0.3 single-window usage shape readable. The JSON
// key remains by_alias for state-file compatibility; runtime semantics are now
// strictly "by exact model".
func (s *UsageState) UnmarshalJSON(raw []byte) error {
	var payload struct {
		Daily   UsageWindow     `json:"daily"`
		Weekly  UsageWindow     `json:"weekly"`
		ByAlias json.RawMessage `json:"by_alias,omitempty"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	s.Daily = payload.Daily
	s.Weekly = payload.Weekly
	s.ByAlias = make(map[string]AliasUsageWindows)
	if len(payload.ByAlias) == 0 || string(payload.ByAlias) == "null" {
		return nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(payload.ByAlias, &entries); err != nil {
		return err
	}
	for model, rawEntry := range entries {
		if len(rawEntry) == 0 || string(rawEntry) == "null" {
			continue
		}
		if hasJSONKey(rawEntry, "daily") || hasJSONKey(rawEntry, "weekly") {
			var windows AliasUsageWindows
			if err := json.Unmarshal(rawEntry, &windows); err == nil {
				s.ByAlias[model] = windows
			}
			continue
		}
		var window UsageWindow
		if err := json.Unmarshal(rawEntry, &window); err == nil {
			s.ByAlias[model] = AliasUsageWindows{Daily: window}
		}
	}
	return nil
}

func hasJSONKey(raw json.RawMessage, key string) bool {
	return bytes.Contains(raw, []byte(`"`+key+`"`))
}

type UsageWindow struct {
	TotalUSD        float64   `json:"total_usd"`
	WindowStart     time.Time `json:"window_start,omitempty"`
	CacheReadTokens int64     `json:"cache_read_tokens,omitempty"`
	CacheCostUSD    float64   `json:"cache_cost_usd,omitempty"`
	InputTokens     int64     `json:"input_tokens,omitempty"`
	OutputTokens    int64     `json:"output_tokens,omitempty"`
	CallCount       int64     `json:"call_count,omitempty"`
}

type State struct {
	Version   int                    `json:"version"`
	Keys      []KeyConfig            `json:"keys"`
	Usage     map[string]*UsageState `json:"usage,omitempty"`
	UpdatedAt time.Time              `json:"updated_at"`

	// Aliases is decoded only when upgrading a v0.4.x state file.
	Aliases []AliasMapping `json:"aliases,omitempty"`
}

func DefaultConfig() Config {
	return Config{Enabled: true, StateFile: "cpa-key-policy-state.json"}
}

func DecodeConfig(raw []byte) (Config, error) {
	cfg := DefaultConfig()
	if len(strings.TrimSpace(string(raw))) == 0 {
		return cfg, nil
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(cfg.StateFile) == "" {
		cfg.StateFile = DefaultConfig().StateFile
	}
	if err := normalizeConfig(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func normalizeConfig(cfg *Config) error {
	legacyAliases := make(map[string]AliasMapping, len(cfg.Aliases))
	for _, alias := range cfg.Aliases {
		name := strings.ToLower(strings.TrimSpace(alias.Alias))
		if name == "" {
			continue
		}
		legacyAliases[name] = alias
	}

	seenIDs := make(map[string]struct{}, len(cfg.Keys))
	for i := range cfg.Keys {
		key := &cfg.Keys[i]
		key.ID = strings.TrimSpace(key.ID)
		key.Name = strings.TrimSpace(key.Name)
		key.KeyHash = strings.TrimSpace(key.KeyHash)
		key.KeyPreview = strings.TrimSpace(key.KeyPreview)
		if key.ID == "" {
			return errors.New("key id is required")
		}
		if _, exists := seenIDs[key.ID]; exists {
			return fmt.Errorf("duplicate key id %q", key.ID)
		}
		seenIDs[key.ID] = struct{}{}
		if key.Name == "" {
			key.Name = key.ID
		}
		if key.RPM < 0 {
			return fmt.Errorf("key %q rpm cannot be negative", key.ID)
		}
		if key.DailyLimitUSD < 0 {
			return fmt.Errorf("key %q daily_limit_usd cannot be negative", key.ID)
		}
		if key.WeeklyLimitUSD < 0 {
			return fmt.Errorf("key %q weekly_limit_usd cannot be negative", key.ID)
		}

		rules := append([]ModelRule(nil), key.Models...)
		for _, ref := range key.Aliases {
			legacy, ok := legacyAliases[strings.ToLower(strings.TrimSpace(ref.Alias))]
			if !ok {
				return fmt.Errorf("key %q references unknown legacy alias %q", key.ID, ref.Alias)
			}
			for _, target := range legacy.Targets {
				rule := ModelRule{
					Model:                    target.TargetModel,
					BillingMode:              legacy.BillingMode,
					InputPricePerMillion:     legacy.InputPricePerMillion,
					OutputPricePerMillion:    legacy.OutputPricePerMillion,
					CacheReadPricePerMillion: legacy.CacheReadPricePerMillion,
					PerCallUSD:               legacy.PerCallUSD,
				}
				if ref.InputPricePerMillion != nil {
					rule.InputPricePerMillion = *ref.InputPricePerMillion
				}
				if ref.OutputPricePerMillion != nil {
					rule.OutputPricePerMillion = *ref.OutputPricePerMillion
				}
				if ref.CacheReadPricePerMillion != nil {
					rule.CacheReadPricePerMillion = *ref.CacheReadPricePerMillion
				}
				if ref.PerCallUSD != nil {
					rule.PerCallUSD = *ref.PerCallUSD
				}
				rules = append(rules, rule)
			}
		}

		normalized := make([]ModelRule, 0, len(rules))
		seenModels := make(map[string]struct{}, len(rules))
		for _, rule := range rules {
			model := strings.TrimSpace(rule.Model)
			if model == "" {
				model = strings.TrimSpace(rule.TargetModel)
			}
			if model == "" {
				model = strings.TrimSpace(rule.Alias)
			}
			if model == "" {
				return fmt.Errorf("key %q model entries require model", key.ID)
			}
			modelKey := strings.ToLower(model)
			if _, duplicate := seenModels[modelKey]; duplicate {
				continue
			}
			seenModels[modelKey] = struct{}{}
			rule.Model = model
			rule.Alias = ""
			rule.Provider = ""
			rule.TargetModel = ""
			rule.Group = ""
			if math.IsNaN(rule.InputPricePerMillion) || math.IsInf(rule.InputPricePerMillion, 0) ||
				math.IsNaN(rule.OutputPricePerMillion) || math.IsInf(rule.OutputPricePerMillion, 0) ||
				math.IsNaN(rule.CacheReadPricePerMillion) || math.IsInf(rule.CacheReadPricePerMillion, 0) ||
				rule.InputPricePerMillion < 0 || rule.OutputPricePerMillion < 0 || rule.CacheReadPricePerMillion < 0 {
				return fmt.Errorf("key %q model %q prices must be finite and non-negative", key.ID, model)
			}
			rule.InputPricePerMillion = normalizePrice(rule.InputPricePerMillion)
			rule.OutputPricePerMillion = normalizePrice(rule.OutputPricePerMillion)
			rule.CacheReadPricePerMillion = normalizePrice(rule.CacheReadPricePerMillion)
			switch strings.ToLower(strings.TrimSpace(rule.BillingMode)) {
			case "", "tokens":
				rule.BillingMode = "tokens"
			case "per_call":
				rule.BillingMode = "per_call"
			default:
				return fmt.Errorf("key %q model %q billing_mode %q must be \"tokens\" or \"per_call\"", key.ID, model, rule.BillingMode)
			}
			if math.IsNaN(rule.PerCallUSD) || math.IsInf(rule.PerCallUSD, 0) || rule.PerCallUSD < 0 {
				return fmt.Errorf("key %q model %q per_call_usd must be finite and non-negative", key.ID, model)
			}
			rule.PerCallUSD = normalizePrice(rule.PerCallUSD)
			normalized = append(normalized, rule)
		}
		key.Models = normalized
		key.Aliases = nil
	}
	cfg.Aliases = nil
	return nil
}

func ResolveStatePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultConfig().StateFile
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func LoadState(path string) (*State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	if state.Version == 0 {
		state.Version = 1
	}
	if state.Usage == nil {
		state.Usage = make(map[string]*UsageState)
	}
	return &state, nil
}

func SaveState(path string, keys []KeyConfig, usage map[string]*UsageState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	cleanKeys := make([]KeyConfig, len(keys))
	for i := range keys {
		cleanKeys[i] = keys[i]
		cleanKeys[i].Models = append([]ModelRule(nil), keys[i].Models...)
		cleanKeys[i].Aliases = nil
	}
	state := State{Version: 2, Keys: cleanKeys, Usage: usage, UpdatedAt: time.Now().UTC()}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteStateFile(path, raw)
}

func SaveUsageOnly(path string, usage map[string]*UsageState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var state State
	if current, err := LoadState(path); err == nil {
		state = *current
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state.Version = 2
	state.Usage = usage
	state.UpdatedAt = time.Now().UTC()
	state.Aliases = nil
	for i := range state.Keys {
		state.Keys[i].Aliases = nil
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteStateFile(path, raw)
}

func atomicWriteStateFile(path string, raw []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

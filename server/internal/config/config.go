// Package config loads Sentinel's settings from the environment and an
// optional .env file, applies defaults and resolves paths.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds every setting from the Configuration table in ARCHITECTURE.md.
type Config struct {
	Port         int
	Timezone     string
	DataDir      string // absolute
	AgentsDir    string // absolute
	CORSOrigin   string
	DashboardURL string

	RunTimeout    time.Duration
	TriageTimeout time.Duration
	DemoReadOnly  bool

	LLMProvider     string
	LLMModel        string
	LLMVision       bool
	GeminiAPIKey    string
	AnthropicAPIKey string

	AgentMaxSteps    int
	AgentMaxCostUSD  float64
	AgentTimeout     time.Duration
	AgentConcurrency int

	SlackWebhookURL string
	SMTPHost        string
	SMTPPort        int
	SMTPUser        string
	SMTPPass        string
	EmailFrom       string
	EmailTo         string
}

// Load reads envFile (if it exists), then builds a Config from the process
// environment. Variables already set in the environment win over the file.
// Relative DATA_DIR and AGENTS_DIR are resolved against baseDir.
func Load(envFile, baseDir string) (Config, error) {
	fileVals, err := readEnvFile(envFile)
	if err != nil {
		return Config{}, err
	}
	get := func(key string) string {
		if v, ok := os.LookupEnv(key); ok {
			return v
		}
		return fileVals[key]
	}
	return build(get, baseDir)
}

// build turns raw values into a Config. get returns "" for unset keys.
func build(get func(string) string, baseDir string) (Config, error) {
	p := parser{get: get}
	c := Config{
		Port:         p.int("PORT", 8080),
		Timezone:     p.str("TIMEZONE", "Asia/Kolkata"),
		DataDir:      p.str("DATA_DIR", "../data"),
		AgentsDir:    p.str("AGENTS_DIR", "../agents"),
		CORSOrigin:   p.str("CORS_ORIGIN", "http://localhost:3000"),
		DashboardURL: p.str("DASHBOARD_URL", "http://localhost:3000"),

		RunTimeout:    p.duration("RUN_TIMEOUT", 15*time.Minute),
		TriageTimeout: p.duration("TRIAGE_TIMEOUT", 3*time.Minute),
		DemoReadOnly:  p.bool("DEMO_READ_ONLY", false),

		LLMProvider:     p.str("LLM_PROVIDER", "gemini"),
		LLMModel:        p.str("LLM_MODEL", ""),
		LLMVision:       p.bool("LLM_VISION", true),
		GeminiAPIKey:    p.str("GEMINI_API_KEY", ""),
		AnthropicAPIKey: p.str("ANTHROPIC_API_KEY", ""),

		AgentMaxSteps:    p.int("AGENT_MAX_STEPS", 80),
		AgentMaxCostUSD:  p.float("AGENT_MAX_COST_USD", 1.00),
		AgentTimeout:     p.duration("AGENT_TIMEOUT", 20*time.Minute),
		AgentConcurrency: p.int("AGENT_CONCURRENCY", 1),

		SlackWebhookURL: p.str("SLACK_WEBHOOK_URL", ""),
		SMTPHost:        p.str("SMTP_HOST", "smtp.gmail.com"),
		SMTPPort:        p.int("SMTP_PORT", 587),
		SMTPUser:        p.str("SMTP_USER", ""),
		SMTPPass:        p.str("SMTP_PASS", ""),
		EmailFrom:       p.str("EMAIL_FROM", ""),
		EmailTo:         p.str("EMAIL_TO", ""),
	}
	if len(p.errs) > 0 {
		return Config{}, fmt.Errorf("config: %w", errors.Join(p.errs...))
	}
	if c.LLMProvider != "gemini" && c.LLMProvider != "anthropic" {
		return Config{}, fmt.Errorf("config: LLM_PROVIDER must be gemini or anthropic, got %q", c.LLMProvider)
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return Config{}, fmt.Errorf("config: TIMEZONE: %w", err)
	}

	var err error
	if c.DataDir, err = absFrom(baseDir, c.DataDir); err != nil {
		return Config{}, fmt.Errorf("config: DATA_DIR: %w", err)
	}
	if c.AgentsDir, err = absFrom(baseDir, c.AgentsDir); err != nil {
		return Config{}, fmt.Errorf("config: AGENTS_DIR: %w", err)
	}
	return c, nil
}

// LogValue makes Config safe to log: secrets are reported only as set or unset.
func (c Config) LogValue() slog.Value {
	set := func(s string) string {
		if s == "" {
			return "unset"
		}
		return "set"
	}
	return slog.GroupValue(
		slog.Int("port", c.Port),
		slog.String("timezone", c.Timezone),
		slog.String("data_dir", c.DataDir),
		slog.String("agents_dir", c.AgentsDir),
		slog.String("cors_origin", c.CORSOrigin),
		slog.String("dashboard_url", c.DashboardURL),
		slog.Duration("run_timeout", c.RunTimeout),
		slog.Duration("triage_timeout", c.TriageTimeout),
		slog.Bool("demo_read_only", c.DemoReadOnly),
		slog.String("llm_provider", c.LLMProvider),
		slog.String("llm_model", c.LLMModel),
		slog.Bool("llm_vision", c.LLMVision),
		slog.String("gemini_api_key", set(c.GeminiAPIKey)),
		slog.String("anthropic_api_key", set(c.AnthropicAPIKey)),
		slog.Int("agent_max_steps", c.AgentMaxSteps),
		slog.Float64("agent_max_cost_usd", c.AgentMaxCostUSD),
		slog.Duration("agent_timeout", c.AgentTimeout),
		slog.Int("agent_concurrency", c.AgentConcurrency),
		slog.String("slack_webhook_url", set(c.SlackWebhookURL)),
		slog.String("smtp_host", c.SMTPHost),
		slog.Int("smtp_port", c.SMTPPort),
		slog.String("smtp_user", set(c.SMTPUser)),
		slog.String("smtp_pass", set(c.SMTPPass)),
		slog.String("email_from", c.EmailFrom),
		slog.String("email_to", c.EmailTo),
	)
}

func absFrom(baseDir, p string) (string, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(baseDir, p)
	}
	return filepath.Abs(p)
}

// readEnvFile parses KEY=VALUE lines. Blank lines and # comments are skipped,
// an optional "export " prefix is allowed, and one pair of matching quotes
// around the value is removed. A missing file is not an error.
func readEnvFile(path string) (map[string]string, error) {
	vals := map[string]string{}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return vals, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: open env file: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("config: %s:%d: expected KEY=VALUE", path, lineNo)
		}
		vals[strings.TrimSpace(key)] = unquote(strings.TrimSpace(val))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("config: read env file: %w", err)
	}
	return vals, nil
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// parser reads typed values and collects every error instead of stopping at
// the first, so one startup shows all bad settings.
type parser struct {
	get  func(string) string
	errs []error
}

func (p *parser) str(key, def string) string {
	if v := p.get(key); v != "" {
		return v
	}
	return def
}

func (p *parser) int(key string, def int) int {
	v := p.get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: %q is not an integer", key, v))
		return def
	}
	return n
}

func (p *parser) float(key string, def float64) float64 {
	v := p.get(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: %q is not a number", key, v))
		return def
	}
	return f
}

func (p *parser) bool(key string, def bool) bool {
	v := p.get(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: %q is not a boolean", key, v))
		return def
	}
	return b
}

func (p *parser) duration(key string, def time.Duration) time.Duration {
	v := p.get(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: %q is not a duration (e.g. 15m)", key, v))
		return def
	}
	return d
}

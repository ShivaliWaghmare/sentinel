package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mapGetter(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestBuildDefaults(t *testing.T) {
	c, err := build(mapGetter(nil), "/repo/server")
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 8080 || c.Timezone != "Asia/Kolkata" || c.RunTimeout != 15*time.Minute ||
		c.AgentMaxCostUSD != 1.0 || !c.LLMVision || c.SMTPPort != 587 {
		t.Errorf("unexpected defaults: %+v", c)
	}
	if c.DataDir != "/repo/data" {
		t.Errorf("DataDir = %q, want /repo/data", c.DataDir)
	}
	if c.AgentsDir != "/repo/agents" {
		t.Errorf("AgentsDir = %q, want /repo/agents", c.AgentsDir)
	}
}

func TestBuildValues(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		check   func(Config) bool
		wantErr string
	}{
		{"port", map[string]string{"PORT": "9000"}, func(c Config) bool { return c.Port == 9000 }, ""},
		{"absolute data dir", map[string]string{"DATA_DIR": "/var/sentinel"}, func(c Config) bool { return c.DataDir == "/var/sentinel" }, ""},
		{"duration", map[string]string{"AGENT_TIMEOUT": "90s"}, func(c Config) bool { return c.AgentTimeout == 90*time.Second }, ""},
		{"bool", map[string]string{"DEMO_READ_ONLY": "true"}, func(c Config) bool { return c.DemoReadOnly }, ""},
		{"bad int", map[string]string{"PORT": "abc"}, nil, "PORT"},
		{"bad duration", map[string]string{"RUN_TIMEOUT": "15"}, nil, "RUN_TIMEOUT"},
		{"bad provider", map[string]string{"LLM_PROVIDER": "openai"}, nil, "LLM_PROVIDER"},
		{"bad timezone", map[string]string{"TIMEZONE": "Mars/Base"}, nil, "TIMEZONE"},
		{"all errors reported", map[string]string{"PORT": "x", "SMTP_PORT": "y"}, nil, "SMTP_PORT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := build(mapGetter(tt.env), "/repo/server")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !tt.check(c) {
				t.Errorf("check failed: %+v", c)
			}
		})
	}
}

func TestLoadEnvFileAndPrecedence(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	content := "# comment\n\nPORT=9100\nexport LLM_MODEL=\"gemini-x\"\nEMAIL_TO='a@b.c'\nCORS_ORIGIN=http://file\n"
	if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORS_ORIGIN", "http://env")

	c, err := Load(envFile, dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 9100 || c.LLMModel != "gemini-x" || c.EmailTo != "a@b.c" {
		t.Errorf("file values not applied: %+v", c)
	}
	if c.CORSOrigin != "http://env" {
		t.Errorf("CORSOrigin = %q, environment should win over .env", c.CORSOrigin)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.env"), "/repo/server"); err != nil {
		t.Fatalf("missing .env should not be an error: %v", err)
	}
}

func TestLoadMalformedFile(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("NOT_A_PAIR\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(envFile, "/repo/server"); err == nil {
		t.Fatal("expected error for malformed line")
	}
}

func TestLogValueHidesSecrets(t *testing.T) {
	c := Config{GeminiAPIKey: "sk-secret", SMTPPass: "hunter2", SlackWebhookURL: "https://hooks.slack.com/x"}
	out := c.LogValue().String()
	for _, secret := range []string{"sk-secret", "hunter2", "hooks.slack.com"} {
		if strings.Contains(out, secret) {
			t.Errorf("log output leaks %q: %s", secret, out)
		}
	}
}

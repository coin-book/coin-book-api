package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/rotisserie/eris"
	"github.com/spf13/viper"
)

const envPrefix = "COINBOOK"

const minJWTSecretLength = 32

type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	Postgres PostgresConfig `mapstructure:"postgres"`
	Log      LogConfig      `mapstructure:"log"`
	JWT      JWTConfig      `mapstructure:"jwt"`
	Storage  StorageConfig  `mapstructure:"storage"`
	Google   GoogleConfig   `mapstructure:"google"`
	Market   MarketConfig   `mapstructure:"market"`
}

type ServerConfig struct {
	Port                   string   `mapstructure:"port"`
	PublicURL              string   `mapstructure:"public_url"`
	CookieSecure           bool     `mapstructure:"cookie_secure"`
	AllowedOrigins         []string `mapstructure:"allowed_origins"`
	AllowedHosts           []string `mapstructure:"allowed_hosts"`
	AuthRateLimitPerMinute int      `mapstructure:"auth_rate_limit_per_minute"`
	ClientIPHeader         string   `mapstructure:"client_ip_header"`
	DocsEnabled            bool     `mapstructure:"docs_enabled"`
	SeedDemo               bool     `mapstructure:"seed_demo"`
	MaxUploadMB            int64    `mapstructure:"max_upload_mb"`
}

type PostgresConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	DBName   string `mapstructure:"dbname"`
	SSLMode  string `mapstructure:"sslmode"`
}

func (p *PostgresConfig) ConnectionString() string {
	sslMode := p.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		url.QueryEscape(p.User), url.QueryEscape(p.Password), p.Host, p.Port, p.DBName, sslMode)
}

type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

type JWTConfig struct {
	Secret             string `mapstructure:"secret"`
	AccessTokenMinutes int    `mapstructure:"access_token_minutes"`
	RefreshTokenHours  int    `mapstructure:"refresh_token_hours"`
}

// GoogleConfig enables "sign in with Google" when ClientID is set.
type GoogleConfig struct {
	ClientID     string `mapstructure:"client_id"`
	ClientSecret string `mapstructure:"client_secret"`
	IssuerURL    string `mapstructure:"issuer_url"`
}

func (g *GoogleConfig) Enabled() bool { return g.ClientID != "" }

// MarketConfig is where prices and exchange rates come from (ADR 209).
type MarketConfig struct {
	Enabled       bool   `mapstructure:"enabled"`
	YahooURL      string `mapstructure:"yahoo_url"`
	CNBURL        string `mapstructure:"cnb_url"`
	UserAgent     string `mapstructure:"user_agent"`
	RefreshAtHour int    `mapstructure:"refresh_at_hour"`
}

type StorageConfig struct {
	ImportsDir string `mapstructure:"imports_dir"`
}

func Load() (*Config, error) {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath("./config")
	v.AddConfigPath("/app/config")
	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return nil, eris.Wrap(err, "read config")
		}
	}
	for _, key := range v.AllKeys() {
		v.Set(key, v.Get(key))
	}
	bindSecrets(v)

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, eris.Wrap(err, "decode config")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Secrets have no default in config.yaml, so AutomaticEnv alone would never see them.
func bindSecrets(v *viper.Viper) {
	for _, key := range []string{"jwt.secret", "postgres.password", "google.client_secret"} {
		_ = v.BindEnv(key)
	}
}

func (c *Config) Validate() error {
	if len(c.JWT.Secret) < minJWTSecretLength {
		return eris.Errorf("config: %s_JWT_SECRET must be at least %d characters", envPrefix, minJWTSecretLength)
	}
	if c.Storage.ImportsDir == "" {
		return eris.New("config: storage.imports_dir is required")
	}
	if c.Market.RefreshAtHour < 0 || c.Market.RefreshAtHour > 23 {
		return eris.New("config: market.refresh_at_hour must be 0-23")
	}
	c.Server.PublicURL = strings.TrimRight(c.Server.PublicURL, "/")
	if c.Google.Enabled() && (c.Google.ClientSecret == "" || c.Server.PublicURL == "") {
		return eris.Errorf("config: Google sign-in needs %s_GOOGLE_CLIENT_SECRET and %s_SERVER_PUBLIC_URL", envPrefix, envPrefix)
	}
	return nil
}

func (s *ServerConfig) MaxUploadBytes() int64 { return s.MaxUploadMB << 20 }

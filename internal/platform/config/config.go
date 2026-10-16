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
}

type ServerConfig struct {
	Port                   string   `mapstructure:"port"`
	CookieSecure           bool     `mapstructure:"cookie_secure"`
	AllowedOrigins         []string `mapstructure:"allowed_origins"`
	AllowedHosts           []string `mapstructure:"allowed_hosts"`
	AuthRateLimitPerMinute int      `mapstructure:"auth_rate_limit_per_minute"`
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
	for _, key := range []string{"jwt.secret", "postgres.password"} {
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
	return nil
}

func (s *ServerConfig) MaxUploadBytes() int64 { return s.MaxUploadMB << 20 }

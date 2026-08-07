package cmd

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/app"
	"github.com/linxin2429/bili_notify/config"
	"github.com/linxin2429/bili_notify/model"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func NewRootCmd() *cobra.Command {
	v := viper.New()
	setDefaults(v)
	v.SetEnvPrefix("BILI_NOTIFY")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()

	root := &cobra.Command{
		Use:           "bili-notify",
		Short:         "Bilibili dynamic notifications",
		Version:       fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newServeCmd(v), newHealthcheckCmd())
	return root
}

func ExecuteContext(ctx context.Context) error { return NewRootCmd().ExecuteContext(ctx) }

func setDefaults(v *viper.Viper) {
	v.SetDefault("data_dir", "/data")
	v.SetDefault("admin_addr", ":8443")
	v.SetDefault("observe_addr", ":9090")
	v.SetDefault("poll_interval", time.Duration(model.DefaultPollIntervalSec)*time.Second)
	v.SetDefault("request_rate", model.DefaultRequestRate)
	v.SetDefault("request_concurrency", model.DefaultRequestConcurrency)
	v.SetDefault("log_level", model.DefaultLogLevel)
	v.SetDefault("audit_log_retention", time.Duration(model.DefaultAuditRetentionDays)*24*time.Hour)
	v.SetDefault("system_log_retention", time.Duration(model.DefaultSystemRetentionDays)*24*time.Hour)
}

func newServeCmd(v *viper.Viper) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the notification service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := bindServeFlags(v, cmd); err != nil {
				return err
			}
			cfg := config.Config{
				DataDir: v.GetString("data_dir"), AdminAddr: v.GetString("admin_addr"), ObserveAddr: v.GetString("observe_addr"),
				PollInterval: v.GetDuration("poll_interval"), RequestRate: v.GetFloat64("request_rate"), RequestConcurrency: v.GetInt("request_concurrency"), LogLevel: v.GetString("log_level"),
				AuditLogRetention: v.GetDuration("audit_log_retention"), SystemLogRetention: v.GetDuration("system_log_retention"),
			}
			return app.Run(cmd.Context(), cfg, version)
		},
	}
	cmd.Flags().String("data-dir", v.GetString("data_dir"), "persistent data directory")
	cmd.Flags().String("admin-addr", v.GetString("admin_addr"), "TLS admin listen address")
	cmd.Flags().String("observe-addr", v.GetString("observe_addr"), "observability listen address")
	cmd.Flags().Duration("poll-interval", v.GetDuration("poll_interval"), "default polling interval for a new data directory")
	cmd.Flags().Float64("request-rate", v.GetFloat64("request_rate"), "default Bilibili requests per second for a new data directory")
	cmd.Flags().Int("request-concurrency", v.GetInt("request_concurrency"), "default maximum concurrent Bilibili requests for a new data directory")
	cmd.Flags().String("log-level", v.GetString("log_level"), "default log level for a new data directory")
	cmd.Flags().Duration("audit-log-retention", v.GetDuration("audit_log_retention"), "default audit log retention for a new data directory")
	cmd.Flags().Duration("system-log-retention", v.GetDuration("system_log_retention"), "default system log retention for a new data directory")
	return cmd
}

func bindServeFlags(v *viper.Viper, cmd *cobra.Command) error {
	bindings := map[string]string{
		"data_dir": "data-dir", "admin_addr": "admin-addr", "observe_addr": "observe-addr",
		"poll_interval": "poll-interval", "request_rate": "request-rate", "request_concurrency": "request-concurrency", "log_level": "log-level",
		"audit_log_retention": "audit-log-retention", "system_log_retention": "system-log-retention",
	}
	for key, flagName := range bindings {
		if err := v.BindPFlag(key, cmd.Flags().Lookup(flagName)); err != nil {
			return fmt.Errorf("binding flag %s: %w", flagName, err)
		}
	}
	return nil
}

func newHealthcheckCmd() *cobra.Command {
	var (
		endpoint string
		method   string
		body     string
		contains string
		insecure bool
	)
	cmd := &cobra.Command{
		Use:   "healthcheck",
		Short: "Check a local HTTP endpoint",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
			defer cancel()
			method = strings.ToUpper(strings.TrimSpace(method))
			if method == "" {
				method = http.MethodGet
			}
			var reqBody io.Reader
			if body != "" {
				reqBody = strings.NewReader(body)
			}
			req, err := http.NewRequestWithContext(ctx, method, endpoint, reqBody)
			if err != nil {
				return err
			}
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			client := http.DefaultClient
			if insecure {
				client = &http.Client{
					Transport: &http.Transport{
						TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // local container smoke probes only
					},
				}
			}
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if err != nil {
				return err
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return fmt.Errorf("endpoint returned HTTP %d", resp.StatusCode)
			}
			if contains != "" && !strings.Contains(string(payload), contains) {
				return fmt.Errorf("response does not contain %q", contains)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&endpoint, "url", "http://127.0.0.1:9090/healthz", "endpoint URL")
	cmd.Flags().StringVar(&method, "method", http.MethodGet, "HTTP method")
	cmd.Flags().StringVar(&body, "body", "", "optional JSON request body")
	cmd.Flags().StringVar(&contains, "contains", "", "optional response body substring that must be present")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "skip TLS certificate verification")
	return cmd
}

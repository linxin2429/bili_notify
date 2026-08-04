package cmd

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/app"
	"github.com/linxin2429/bili_notify/config"
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
	v.SetDefault("poll_interval", 30*time.Second)
	v.SetDefault("request_rate", 2.0)
	v.SetDefault("request_concurrency", 4)
	v.SetDefault("log_level", "info")
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
			}
			return app.Run(cmd.Context(), cfg, version)
		},
	}
	cmd.Flags().String("data-dir", v.GetString("data_dir"), "persistent data directory")
	cmd.Flags().String("admin-addr", v.GetString("admin_addr"), "TLS admin listen address")
	cmd.Flags().String("observe-addr", v.GetString("observe_addr"), "observability listen address")
	cmd.Flags().Duration("poll-interval", v.GetDuration("poll_interval"), "target polling interval")
	cmd.Flags().Float64("request-rate", v.GetFloat64("request_rate"), "Bilibili requests per second")
	cmd.Flags().Int("request-concurrency", v.GetInt("request_concurrency"), "maximum concurrent Bilibili requests")
	cmd.Flags().String("log-level", v.GetString("log_level"), "debug, info, warn, or error")
	return cmd
}

func bindServeFlags(v *viper.Viper, cmd *cobra.Command) error {
	bindings := map[string]string{
		"data_dir": "data-dir", "admin_addr": "admin-addr", "observe_addr": "observe-addr",
		"poll_interval": "poll-interval", "request_rate": "request-rate", "request_concurrency": "request-concurrency", "log_level": "log-level",
	}
	for key, flagName := range bindings {
		if err := v.BindPFlag(key, cmd.Flags().Lookup(flagName)); err != nil {
			return fmt.Errorf("binding flag %s: %w", flagName, err)
		}
	}
	return nil
}

func newHealthcheckCmd() *cobra.Command {
	var endpoint string
	cmd := &cobra.Command{
		Use:   "healthcheck",
		Short: "Check the local liveness endpoint",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			if err != nil {
				return err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("health endpoint returned HTTP %d", resp.StatusCode)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&endpoint, "url", "http://127.0.0.1:9090/healthz", "health endpoint URL")
	return cmd
}

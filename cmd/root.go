package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/app"
	"github.com/linxin2429/bili_notify/config"
	"github.com/linxin2429/bili_notify/state"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/linxin2429/bili_notify/web"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/term"
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
	root.AddCommand(newServeCmd(v), newAdminCmd(), newHealthcheckCmd(), newRekeyCmd(v))
	return root
}

func ExecuteContext(ctx context.Context) error { return NewRootCmd().ExecuteContext(ctx) }

func setDefaults(v *viper.Viper) {
	v.SetDefault("data_path", "/data/bili-notify.db")
	v.SetDefault("admin_addr", ":8443")
	v.SetDefault("observe_addr", ":9090")
	v.SetDefault("tls_cert_file", "/run/secrets/tls.crt")
	v.SetDefault("tls_key_file", "/run/secrets/tls.key")
	v.SetDefault("master_key_file", "/run/secrets/master-key")
	v.SetDefault("admin_hash_file", "/run/secrets/admin-password-hash")
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
				DataPath: v.GetString("data_path"), AdminAddr: v.GetString("admin_addr"), ObserveAddr: v.GetString("observe_addr"),
				TLSCertFile: v.GetString("tls_cert_file"), TLSKeyFile: v.GetString("tls_key_file"), MasterKeyFile: v.GetString("master_key_file"), AdminHashFile: v.GetString("admin_hash_file"),
				PollInterval: v.GetDuration("poll_interval"), RequestRate: v.GetFloat64("request_rate"), RequestConcurrency: v.GetInt("request_concurrency"), LogLevel: v.GetString("log_level"),
			}
			return app.Run(cmd.Context(), cfg, version)
		},
	}
	cmd.Flags().String("data-path", v.GetString("data_path"), "bbolt database path")
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
		"data_path": "data-path", "admin_addr": "admin-addr", "observe_addr": "observe-addr",
		"poll_interval": "poll-interval", "request_rate": "request-rate", "request_concurrency": "request-concurrency", "log_level": "log-level",
	}
	for key, flagName := range bindings {
		if err := v.BindPFlag(key, cmd.Flags().Lookup(flagName)); err != nil {
			return fmt.Errorf("binding flag %s: %w", flagName, err)
		}
	}
	return nil
}

func newAdminCmd() *cobra.Command {
	admin := &cobra.Command{Use: "admin", Short: "Administrator credential utilities", Args: cobra.NoArgs}
	admin.AddCommand(newHashPasswordCmd())
	return admin
}

func newHashPasswordCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "hash-password",
		Short: "Generate an Argon2id administrator password hash",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			password, confirm, err := readPasswordPair(cmd)
			if err != nil {
				return err
			}
			if password != confirm {
				return errors.New("passwords do not match")
			}
			hash, err := web.HashPassword(password)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), hash)
			return err
		},
	}
}

func readPasswordPair(cmd *cobra.Command) (string, string, error) {
	if file, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), "Password: ")
		password, err := term.ReadPassword(int(file.Fd()))
		_, _ = fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return "", "", err
		}
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), "Confirm password: ")
		confirm, err := term.ReadPassword(int(file.Fd()))
		_, _ = fmt.Fprintln(cmd.ErrOrStderr())
		return string(password), string(confirm), err
	}
	reader := bufio.NewReader(cmd.InOrStdin())
	password, err := reader.ReadString('\n')
	if err != nil {
		return "", "", err
	}
	confirm, err := reader.ReadString('\n')
	return strings.TrimSpace(password), strings.TrimSpace(confirm), err
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

func newRekeyCmd(v *viper.Viper) *cobra.Command {
	var newKeyFile string
	cmd := &cobra.Command{
		Use:   "rekey",
		Short: "Re-encrypt state with a new master key while the service is stopped",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			oldKey, err := config.ReadMasterKey(v.GetString("master_key_file"))
			if err != nil {
				return err
			}
			newKey, err := config.ReadMasterKey(newKeyFile)
			if err != nil {
				return err
			}
			oldVault, _ := vault.New(oldKey)
			newVault, _ := vault.New(newKey)
			store, err := state.Open(v.GetString("data_path"), oldVault)
			if err != nil {
				return err
			}
			defer store.Close()
			return store.Rekey(newVault)
		},
	}
	cmd.Flags().StringVar(&newKeyFile, "new-key-file", "", "file containing the new base64 master key")
	_ = cmd.MarkFlagRequired("new-key-file")
	return cmd
}

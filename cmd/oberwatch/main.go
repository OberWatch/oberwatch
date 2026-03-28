// Package main is the entry point for the oberwatch binary.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/OberWatch/oberwatch/internal/alert"
	"github.com/OberWatch/oberwatch/internal/api"
	"github.com/OberWatch/oberwatch/internal/budget"
	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/dashboard"
	"github.com/OberWatch/oberwatch/internal/pricing"
	"github.com/OberWatch/oberwatch/internal/proxy"
	"github.com/OberWatch/oberwatch/internal/storage"
	"github.com/spf13/cobra"
)

var (
	version = "v0.1.0"
	channel = "dev"
	commit  = "dev"
	built   = "unknown"
)

type rootOptions struct {
	configPath  string
	logLevel    string
	logFormat   string
	showVersion bool
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	return newRootCmd().Execute()
}

func newRootCmd() *cobra.Command {
	opts := &rootOptions{
		configPath: "",
		logLevel:   string(config.LogLevelInfo),
		logFormat:  string(config.LogFormatText),
	}

	rootCmd := &cobra.Command{
		Use:          "oberwatch",
		Short:        "Oberwatch — proxy and observability platform for AI agents",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			printed, err := maybePrintVersion(cmd.OutOrStdout(), opts.showVersion)
			if printed || err != nil {
				return err
			}
			return cmd.Help()
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if opts.showVersion {
				return nil
			}
			return validateGlobalFlags(opts.logLevel, opts.logFormat)
		},
	}

	rootCmd.PersistentFlags().StringVarP(&opts.configPath, "config", "c", "", "Path to config file")
	rootCmd.PersistentFlags().StringVar(&opts.logLevel, "log-level", "info", "Log level: debug, info, warn, error")
	rootCmd.PersistentFlags().StringVar(&opts.logFormat, "log-format", "text", "Log format: text, json")
	rootCmd.PersistentFlags().BoolVarP(&opts.showVersion, "version", "v", false, "Print version and exit")

	rootCmd.AddCommand(
		newServeCmd(opts),
		newGateCmd(opts),
		newTraceCmd(opts),
		newTestCmd(opts),
		newValidateCmd(opts),
		newInitCmd(opts),
		newVersionCmd(),
	)

	return rootCmd
}

type serveOptions struct {
	host        string
	adminToken  string
	port        int
	noDashboard bool
	noTrace     bool
	noGate      bool
}

func newServeCmd(rootOpts *rootOptions) *cobra.Command {
	opts := &serveOptions{
		port: 8080,
		host: "0.0.0.0",
	}

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start all services in one process",
		RunE: func(cmd *cobra.Command, args []string) error {
			printed, err := maybePrintVersion(cmd.OutOrStdout(), rootOpts.showVersion)
			if printed || err != nil {
				return err
			}

			cfg, configLabel, err := loadRuntimeConfig(cmd, rootOpts)
			if err != nil {
				return err
			}
			if validateErr := validatePort(opts.port, "serve --port"); validateErr != nil {
				return validateErr
			}

			host := cfg.Server.Host
			if cmd.Flags().Changed("host") {
				host = opts.host
			}
			port := cfg.Server.Port
			if cmd.Flags().Changed("port") {
				port = opts.port
			}
			if cmd.Flags().Changed("admin-token") {
				cfg.Server.AdminToken = opts.adminToken
			}

			dashboardEnabled := cfg.Server.Dashboard
			gateEnabled := cfg.Gate.Enabled
			traceEnabled := cfg.Trace.Enabled
			if opts.noDashboard {
				dashboardEnabled = false
			}
			if opts.noGate {
				gateEnabled = false
			}
			if opts.noTrace {
				traceEnabled = false
			}

			signalCtx, stopSignals := setupSignalHandling(cmd.Context(), cmd.ErrOrStderr())
			defer stopSignals()

			logger := newLogger(cfg.Server.LogLevel, cfg.Server.LogFormat, cmd.ErrOrStderr())

			if _, err = fmt.Fprint(cmd.OutOrStdout(), renderStartupBanner(startupBannerOptions{
				host:             host,
				configPath:       configLabel,
				port:             port,
				dashboardEnabled: dashboardEnabled,
				gateEnabled:      gateEnabled,
				traceEnabled:     traceEnabled,
			})); err != nil {
				return err
			}
			if isTestMode() {
				return nil
			}

			return runServeRuntime(signalCtx, cfg, logger, host, port, dashboardEnabled, gateEnabled)
		},
	}

	cmd.Flags().IntVarP(&opts.port, "port", "p", 8080, "Listen port")
	cmd.Flags().StringVar(&opts.host, "host", "0.0.0.0", "Bind address")
	cmd.Flags().StringVar(&opts.adminToken, "admin-token", "", "Admin token for management API")
	cmd.Flags().BoolVar(&opts.noDashboard, "no-dashboard", false, "Disable embedded dashboard")
	cmd.Flags().BoolVar(&opts.noTrace, "no-trace", false, "Disable trace collection")
	cmd.Flags().BoolVar(&opts.noGate, "no-gate", false, "Disable cost governance")

	return cmd
}

type gateOptions struct {
	host       string
	adminToken string
	port       int
}

func newGateCmd(rootOpts *rootOptions) *cobra.Command {
	opts := &gateOptions{
		port: 8080,
		host: "0.0.0.0",
	}

	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Start only the gate proxy",
		RunE: func(cmd *cobra.Command, args []string) error {
			printed, err := maybePrintVersion(cmd.OutOrStdout(), rootOpts.showVersion)
			if printed || err != nil {
				return err
			}

			cfg, configLabel, err := loadRuntimeConfig(cmd, rootOpts)
			if err != nil {
				return err
			}
			if validateErr := validatePort(opts.port, "gate --port"); validateErr != nil {
				return validateErr
			}

			host := cfg.Server.Host
			if cmd.Flags().Changed("host") {
				host = opts.host
			}
			port := cfg.Server.Port
			if cmd.Flags().Changed("port") {
				port = opts.port
			}
			if cmd.Flags().Changed("admin-token") {
				_ = opts.adminToken
			}

			_, stopSignals := setupSignalHandling(cmd.Context(), cmd.ErrOrStderr())
			defer stopSignals()

			_, err = fmt.Fprint(cmd.OutOrStdout(), renderStartupBanner(startupBannerOptions{
				host:             host,
				configPath:       configLabel,
				port:             port,
				dashboardEnabled: false,
				gateEnabled:      true,
				traceEnabled:     false,
			}))
			return err
		},
	}

	cmd.Flags().IntVarP(&opts.port, "port", "p", 8080, "Listen port")
	cmd.Flags().StringVar(&opts.host, "host", "0.0.0.0", "Bind address")
	cmd.Flags().StringVar(&opts.adminToken, "admin-token", "", "Admin token for management API")

	return cmd
}

type traceOptions struct {
	storage   string
	dbPath    string
	retention string
	port      int
}

func newTraceCmd(rootOpts *rootOptions) *cobra.Command {
	opts := &traceOptions{
		port:      8081,
		storage:   "sqlite",
		dbPath:    "./oberwatch.db",
		retention: "168h",
	}

	cmd := &cobra.Command{
		Use:   "trace",
		Short: "Start only the trace collector",
		RunE: func(cmd *cobra.Command, args []string) error {
			printed, err := maybePrintVersion(cmd.OutOrStdout(), rootOpts.showVersion)
			if printed || err != nil {
				return err
			}

			cfg, _, err := loadRuntimeConfig(cmd, rootOpts)
			if err != nil {
				return err
			}

			port := opts.port
			storage := string(cfg.Trace.Storage)
			dbPath := cfg.Trace.SQLitePath
			retention := cfg.Trace.Retention

			if cmd.Flags().Changed("storage") {
				storage = opts.storage
			}
			if cmd.Flags().Changed("db-path") {
				dbPath = opts.dbPath
			}
			if cmd.Flags().Changed("retention") {
				retention = opts.retention
			}

			if err := validatePort(port, "trace --port"); err != nil {
				return err
			}
			if storage != string(config.TraceStorageMemory) && storage != string(config.TraceStorageSQLite) {
				return fmt.Errorf("trace --storage must be one of memory, sqlite, got %q", storage)
			}
			if _, err := parsePositiveDuration(retention, "trace --retention"); err != nil {
				return err
			}

			_ = dbPath
			_, stopSignals := setupSignalHandling(cmd.Context(), cmd.ErrOrStderr())
			defer stopSignals()

			return nil
		},
	}

	cmd.Flags().IntVarP(&opts.port, "port", "p", 8081, "Listen port for trace API")
	cmd.Flags().StringVar(&opts.storage, "storage", "sqlite", "Storage backend: memory, sqlite")
	cmd.Flags().StringVar(&opts.dbPath, "db-path", "./oberwatch.db", "SQLite database path")
	cmd.Flags().StringVar(&opts.retention, "retention", "168h", "Trace retention period")

	return cmd
}

func newTestCmd(rootOpts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Run behavioral test scenarios",
		RunE: func(cmd *cobra.Command, args []string) error {
			printed, err := maybePrintVersion(cmd.OutOrStdout(), rootOpts.showVersion)
			if printed || err != nil {
				return err
			}
			return cmd.Help()
		},
	}

	cmd.AddCommand(newTestRunCmd(rootOpts))

	return cmd
}

type testRunOptions struct {
	output      string
	outputFile  string
	filter      string
	proxyURL    string
	judgeModel  string
	judgeKey    string
	timeout     string
	concurrency int
	failFast    bool
	dryRun      bool
}

func newTestRunCmd(rootOpts *rootOptions) *cobra.Command {
	opts := &testRunOptions{
		concurrency: 4,
		timeout:     "30s",
		output:      "console",
		proxyURL:    "http://localhost:8080",
		judgeModel:  "claude-haiku-4-5",
	}

	cmd := &cobra.Command{
		Use:   "run [files/dirs...]",
		Short: "Run scenarios",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			printed, err := maybePrintVersion(cmd.OutOrStdout(), rootOpts.showVersion)
			if printed || err != nil {
				return err
			}

			cfg, _, err := loadRuntimeConfig(cmd, rootOpts)
			if err != nil {
				return err
			}

			targets := args
			if len(targets) == 0 {
				targets = []string{cfg.Test.ScenariosDir}
			}

			concurrency := cfg.Test.Concurrency
			timeout := cfg.Test.Timeout
			output := opts.output
			judgeModel := cfg.Test.Judge.Model
			judgeKey := cfg.Test.Judge.APIKey

			if cmd.Flags().Changed("concurrency") {
				concurrency = opts.concurrency
			}
			if cmd.Flags().Changed("timeout") {
				timeout = opts.timeout
			}
			if cmd.Flags().Changed("judge-model") {
				judgeModel = opts.judgeModel
			}
			if cmd.Flags().Changed("judge-key") {
				judgeKey = opts.judgeKey
			}

			if concurrency < 1 {
				return fmt.Errorf("test run --concurrency must be at least 1")
			}
			if _, err := parsePositiveDuration(timeout, "test run --timeout"); err != nil {
				return err
			}
			if !isOneOf(output, "console", "junit", "json") {
				return fmt.Errorf("test run --output must be one of console, junit, json, got %q", output)
			}
			if opts.filter != "" {
				if _, err := regexp.Compile(opts.filter); err != nil {
					return fmt.Errorf("test run --filter invalid regex: %w", err)
				}
			}

			_, stopSignals := setupSignalHandling(cmd.Context(), cmd.ErrOrStderr())
			defer stopSignals()

			_, _, _, _, _, _, _, _ = targets, output, judgeModel, judgeKey, opts.proxyURL, opts.failFast, opts.dryRun, opts.outputFile
			return nil
		},
	}

	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 4, "Parallel test execution")
	cmd.Flags().StringVar(&opts.timeout, "timeout", "30s", "Timeout per scenario")
	cmd.Flags().StringVarP(&opts.output, "output", "o", "console", "Output format: console, junit, json")
	cmd.Flags().StringVar(&opts.outputFile, "output-file", "", "Write output to file")
	cmd.Flags().BoolVar(&opts.failFast, "fail-fast", false, "Stop on first failure")
	cmd.Flags().StringVar(&opts.filter, "filter", "", "Run scenarios matching regex")
	cmd.Flags().StringVar(&opts.proxyURL, "proxy-url", "http://localhost:8080", "Oberwatch proxy URL")
	cmd.Flags().StringVar(&opts.judgeModel, "judge-model", "claude-haiku-4-5", "Model for LLM-as-judge")
	cmd.Flags().StringVar(&opts.judgeKey, "judge-key", "", "API key for judge model")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Parse and validate without executing")

	return cmd
}

func newValidateCmd(rootOpts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate a config file",
		RunE: func(cmd *cobra.Command, args []string) error {
			printed, err := maybePrintVersion(cmd.OutOrStdout(), rootOpts.showVersion)
			if printed || err != nil {
				return err
			}
			_, err = config.Load(rootOpts.configPath)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "config %s is valid\n", rootOpts.configPath)
			return err
		},
	}
}

type initOptions struct {
	output string
	force  bool
}

func newInitCmd(rootOpts *rootOptions) *cobra.Command {
	opts := &initOptions{
		output: "./oberwatch.toml",
	}

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate a starter config file",
		RunE: func(cmd *cobra.Command, args []string) error {
			printed, err := maybePrintVersion(cmd.OutOrStdout(), rootOpts.showVersion)
			if printed || err != nil {
				return err
			}

			if writeErr := writeStarterConfig(opts.output, opts.force); writeErr != nil {
				return writeErr
			}

			_, err = fmt.Fprintf(cmd.OutOrStdout(), "wrote starter config to %s\n", opts.output)
			return err
		},
	}

	cmd.Flags().StringVarP(&opts.output, "output", "o", "./oberwatch.toml", "Output path")
	cmd.Flags().BoolVar(&opts.force, "force", false, "Overwrite existing config file")

	return cmd
}

func writeStarterConfig(path string, force bool) error {
	if !force {
		return config.GenerateStarter(path)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory for %q: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(config.StarterTOML), 0o644); err != nil {
		return fmt.Errorf("write starter config %q: %w", path, err)
	}

	return nil
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, args []string) error {
			return printVersion(cmd.OutOrStdout())
		},
	}
}

func printVersion(w io.Writer) error {
	_, err := fmt.Fprintf(
		w,
		"oberwatch %s\ncommit: %s\nbuilt: %s\ngo: %s\nos/arch: %s/%s\n",
		displayVersion(),
		commit,
		built,
		runtime.Version(),
		runtime.GOOS,
		runtime.GOARCH,
	)
	return err
}

func maybePrintVersion(w io.Writer, showVersion bool) (bool, error) {
	if !showVersion {
		return false, nil
	}

	return true, printVersion(w)
}

func displayVersion() string {
	cleanVersion := strings.TrimSpace(version)
	if cleanVersion == "" {
		cleanVersion = "v0.0.0"
	}

	cleanChannel := strings.TrimSpace(channel)
	if cleanChannel == "" {
		return cleanVersion
	}

	return fmt.Sprintf("%s (%s)", cleanVersion, cleanChannel)
}

func loadRuntimeConfig(cmd *cobra.Command, rootOpts *rootOptions) (config.Config, string, error) {
	cfg, configLabel, err := config.LoadRuntime(rootOpts.configPath)
	if err != nil {
		return config.Config{}, "", err
	}
	if cmd.Flags().Changed("log-level") {
		cfg.Server.LogLevel = config.LogLevel(rootOpts.logLevel)
	}
	if cmd.Flags().Changed("log-format") {
		cfg.Server.LogFormat = config.LogFormat(rootOpts.logFormat)
	}

	return cfg, configLabel, nil
}

func validateGlobalFlags(logLevel, logFormat string) error {
	if !isOneOf(strings.ToLower(logLevel), string(config.LogLevelDebug), string(config.LogLevelInfo), string(config.LogLevelWarn), string(config.LogLevelError)) {
		return fmt.Errorf("--log-level must be one of debug, info, warn, error, got %q", logLevel)
	}
	if !isOneOf(strings.ToLower(logFormat), string(config.LogFormatText), string(config.LogFormatJSON)) {
		return fmt.Errorf("--log-format must be one of text, json, got %q", logFormat)
	}
	return nil
}

func validatePort(port int, flagName string) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%s must be between 1 and 65535, got %d", flagName, port)
	}
	return nil
}

func parsePositiveDuration(raw string, field string) (time.Duration, error) {
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration: %w", field, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be greater than 0, got %s", field, raw)
	}
	return duration, nil
}

func isOneOf(got string, allowed ...string) bool {
	for _, candidate := range allowed {
		if got == candidate {
			return true
		}
	}
	return false
}

func setupSignalHandling(parent context.Context, stderr io.Writer) (context.Context, func()) {
	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-hupCh:
				_, _ = fmt.Fprintln(stderr, "received SIGHUP, config reload placeholder")
			}
		}
	}()

	cleanup := func() {
		signal.Stop(hupCh)
		stop()
		<-done
	}

	return ctx, cleanup
}

type alertDispatchFunc func(context.Context, alert.Alert)

func (f alertDispatchFunc) Dispatch(ctx context.Context, entry alert.Alert) {
	f(ctx, entry)
}

func isTestMode() bool {
	return os.Getenv("OW_TEST_MODE") == "1"
}

func newLogger(level config.LogLevel, format config.LogFormat, output io.Writer) *slog.Logger {
	var slogLevel slog.Level
	switch level {
	case config.LogLevelDebug:
		slogLevel = slog.LevelDebug
	case config.LogLevelWarn:
		slogLevel = slog.LevelWarn
	case config.LogLevelError:
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}

	handlerOptions := &slog.HandlerOptions{Level: slogLevel}
	if format == config.LogFormatJSON {
		return slog.New(slog.NewJSONHandler(output, handlerOptions))
	}
	return slog.New(slog.NewTextHandler(output, handlerOptions))
}

func runServeRuntime(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	host string,
	port int,
	dashboardEnabled bool,
	gateEnabled bool,
) error {
	retention, err := parsePositiveDuration(cfg.Trace.Retention, "trace.retention")
	if err != nil {
		return err
	}

	store, err := storage.NewSQLiteStore(cfg.Trace.SQLitePath, retention, logger)
	if err != nil {
		return fmt.Errorf("initialize sqlite storage: %w", err)
	}
	defer func() {
		_ = store.Close()
	}()

	bufferSize := cfg.Trace.MemoryBufferSize
	if bufferSize <= 0 {
		bufferSize = 1000
	}
	costWriter := storage.NewBufferedCostWriter(store, bufferSize, logger)
	defer costWriter.Close()

	baseDispatcher := alert.NewDispatcher(cfg.Alerts, 5*time.Second, logger)
	var managementServer *api.Server
	dispatcher := alertDispatchFunc(func(dispatchCtx context.Context, entry alert.Alert) {
		if managementServer != nil {
			managementServer.PublishAlert(entry)
		}
		baseDispatcher.Dispatch(dispatchCtx, entry)
	})

	budgetManager := budget.NewManagerWithClockAndDispatcher(cfg.Gate, logger, nil, dispatcher)
	managementServer = api.New(cfg, budgetManager, store, displayVersion())

	hooks := proxy.Hooks{
		Management: managementServer,
		Logger:     logger,
	}
	if gateEnabled {
		hooks.Budget = budgetManager
		hooks.Pricing = pricing.NewPricingTableFromConfig(cfg.Pricing, logger)
		hooks.CostSink = managementServer.WrapCostSink(costWriter)
	}

	if dashboardEnabled {
		dashboardHandler, handlerErr := dashboard.NewHandler()
		if handlerErr != nil {
			return fmt.Errorf("load embedded dashboard: %w", handlerErr)
		}
		hooks.Dashboard = dashboardHandler
	}

	proxyServer, err := proxy.New(cfg, hooks)
	if err != nil {
		return fmt.Errorf("initialize proxy server: %w", err)
	}

	httpServer := &http.Server{
		Addr:              net.JoinHostPort(host, strconv.Itoa(port)),
		Handler:           proxyServer,
		ReadHeaderTimeout: 10 * time.Second,
	}

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve proxy: %w", err)
	}

	<-shutdownDone
	return nil
}

type startupBannerOptions struct {
	host             string
	configPath       string
	port             int
	dashboardEnabled bool
	gateEnabled      bool
	traceEnabled     bool
}

func renderStartupBanner(opts startupBannerOptions) string {
	proxyURL := fmt.Sprintf("http://%s:%d", opts.host, opts.port)
	dashboard := "disabled"
	if opts.dashboardEnabled {
		dashboard = proxyURL
	}
	return fmt.Sprintf(
		" ╔═══════════════════════════════════════╗\n"+
			" ║          Oberwatch %-18s║\n"+
			" ╠═══════════════════════════════════════╣\n"+
			" ║  Proxy:     %-27s║\n"+
			" ║  Dashboard: %-27s║\n"+
			" ║  Gate:      %-27s║\n"+
			" ║  Trace:     %-27s║\n"+
			" ║  Config:    %-27s║\n"+
			" ╚═══════════════════════════════════════╝\n",
		version,
		proxyURL,
		dashboard,
		boolState(opts.gateEnabled),
		boolState(opts.traceEnabled),
		opts.configPath,
	)
}

func boolState(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

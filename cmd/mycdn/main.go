package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"mycdn/internal/admin"
	"mycdn/internal/analytics"
	"mycdn/internal/cache"
	"mycdn/internal/config"
	"mycdn/internal/gateway"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout)
	stop()
	if err != nil {
		log.Print(err)
		// All application resources are closed by run before returning.
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) (result error) {
	flags := flag.NewFlagSet("mycdn", flag.ContinueOnError)
	flags.SetOutput(stdout)
	configPath := flags.String("config", "data/config.json", "Configuration document (created if missing)")
	proxyListen := flags.String("proxy", "", "Override the proxy listen address for this process")
	adminListen := flags.String("admin", "", "Override the management listen address for this process")
	dataDir := flags.String("data", "", "Override the cache/analytics data directory for this process")
	openAPI := flags.Bool("openapi", false, "Print generated OpenAPI JSON and exit without opening stores")
	showVersion := flags.Bool("version", false, "Print the binary version and exit")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *openAPI {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(admin.OpenAPI())
	}
	if *showVersion {
		_, err := fmt.Fprintln(stdout, version)
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	auth, err := admin.NewAuth(os.Getenv("MYCDN_ADMIN_KEY"))
	if err != nil {
		return err
	}
	defer auth.Close()
	path, err := filepath.Abs(*configPath)
	if err != nil {
		return err
	}
	if err := createInitialConfig(path); err != nil {
		return fmt.Errorf("create configuration: %w", err)
	}
	configuration, err := config.NewStore(path)
	if err != nil {
		return fmt.Errorf("open configuration: %w", err)
	}
	document := configuration.Get().Document()
	running := admin.Options{Version: version, StartedAt: time.Now(), ProxyListen: document.Listen.Proxy, AdminListen: document.Listen.Admin, DataDir: document.DataDir}
	if *proxyListen != "" {
		running.ProxyListen = *proxyListen
	}
	if *adminListen != "" {
		running.AdminListen = *adminListen
	}
	if *dataDir != "" {
		running.DataDir = *dataDir
	}
	running.DataDir, err = filepath.Abs(running.DataDir)
	if err != nil {
		return err
	}
	objects, err := cache.Open(cacheOptions(running.DataDir, document.Storage))
	if err != nil {
		return fmt.Errorf("open cache: %w", err)
	}
	var events *analytics.Store
	var proxy *gateway.Gateway
	var management *admin.Server
	// Shutdown listeners and handlers below before unwinding this ownership
	// stack: shared fills, gateway sampler/transports, then analytics ingestion.
	defer func() {
		if management != nil {
			result = errors.Join(result, management.Close())
		}
		result = errors.Join(result, objects.Close())
		if proxy != nil {
			result = errors.Join(result, proxy.Close())
		}
		if events != nil {
			result = errors.Join(result, events.Close())
		}
	}()
	if document.Analytics.Enabled {
		events, err = analytics.Open(analyticsOptions(running.DataDir, document.Analytics))
		if err != nil {
			return fmt.Errorf("open analytics: %w", err)
		}
	}
	proxy = gateway.New(configuration, objects, analyticsObserver(events))
	management = admin.New(configuration, objects, proxy, events, running, auth)
	return serve(ctx, running, proxy, management)
}

// createInitialConfig deliberately precedes NewStore: the binary's default
// management listener must be reachable inside a container, and its data_dir is
// the configuration's parent directory, never an extra nested "data" directory.
func createInitialConfig(path string) (result error) {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	document := config.Default()
	document.Listen.Admin = ":8081"
	document.DataDir = filepath.Dir(path)
	file, err := os.CreateTemp(filepath.Dir(path), ".config-init-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer func() {
		if file != nil {
			result = errors.Join(result, file.Close())
		}
	}()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	temporary := file.Name()
	if err := file.Close(); err != nil {
		file = nil
		return err
	}
	file = nil
	// Link publishes a complete file atomically without overwriting a config
	// created concurrently. Both paths are on the same filesystem.
	if err := os.Link(temporary, path); errors.Is(err, os.ErrExist) {
		return nil
	} else if err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func cacheOptions(dir string, settings config.Storage) cache.Options {
	return cache.Options{
		Dir: filepath.Join(dir, "cache"), MaxBytes: settings.MaxBytes, MemoryBytes: settings.MemoryBytes,
		MaxFills: settings.MaxFills, MaxBackground: settings.MaxBackground, MaxObjectBytes: settings.MaxObjectBytes,
		InlineBytes: settings.InlineBytes, BlockBytes: settings.BlockBytes,
		MaxReaders: settings.MaxReaders, FillTimeout: time.Duration(settings.FillTimeoutMS) * time.Millisecond,
	}
}

func analyticsOptions(dir string, settings config.Analytics) analytics.Options {
	return analytics.Options{
		Dir: filepath.Join(dir, "analytics"), MemoryLimitBytes: settings.MemoryBytes, TempLimitBytes: settings.TempBytes,
		DiskBudgetBytes: settings.MaxBytes, DetailRetention: time.Duration(settings.DetailDays) * 24 * time.Hour,
		RollupRetention: time.Duration(settings.RollupDays) * 24 * time.Hour, QueueCapacity: settings.QueueSize,
		BatchSize: settings.BatchSize, FlushInterval: time.Duration(settings.FlushIntervalMS) * time.Millisecond,
		QueryConcurrency: settings.QueryConcurrency, QueryTimeout: time.Duration(settings.QueryTimeoutMS) * time.Millisecond,
		Threads: settings.Threads, CountryMMDB: settings.CountryMMDB, ASNMMDB: settings.ASNMMDB,
	}
}

func serve(ctx context.Context, options admin.Options, proxy, management http.Handler) error {
	proxyListener, err := net.Listen("tcp", options.ProxyListen)
	if err != nil {
		return fmt.Errorf("listen on proxy address: %w", err)
	}
	adminListener, err := net.Listen("tcp", options.AdminListen)
	if err != nil {
		proxyListener.Close()
		return fmt.Errorf("listen on management address: %w", err)
	}
	// The shutdown budget cancels active requests only after a graceful drain.
	// A whole-request ReadTimeout or WriteTimeout would break long-lived streams.
	requests, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	var handlers sync.WaitGroup
	var admission sync.Mutex
	stopped := false
	track := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			admission.Lock()
			if stopped {
				admission.Unlock()
				http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
				return
			}
			handlers.Add(1)
			admission.Unlock()
			defer handlers.Done()
			next.ServeHTTP(w, r)
		})
	}
	proxyProtocols := new(http.Protocols)
	proxyProtocols.SetHTTP1(true)
	proxyProtocols.SetUnencryptedHTTP2(true)
	adminProtocols := new(http.Protocols)
	adminProtocols.SetHTTP1(true)
	servers := []*http.Server{
		{Handler: track(proxy), Protocols: proxyProtocols, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, BaseContext: func(net.Listener) context.Context { return requests }},
		{Handler: track(management), Protocols: adminProtocols, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, BaseContext: func(net.Listener) context.Context { return requests }},
	}
	listeners := []net.Listener{proxyListener, adminListener}
	failures := make(chan error, len(servers))
	for i, server := range servers {
		go func() { failures <- server.Serve(listeners[i]) }()
	}
	log.Printf("mycdn %s proxy=%s admin=%s data=%s", options.Version, proxyListener.Addr(), adminListener.Addr(), options.DataDir)
	var result error
	select {
	case <-ctx.Done():
	case err := <-failures:
		if !errors.Is(err, http.ErrServerClosed) {
			result = err
		}
	}
	drain, cancelDrain := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancelDrain()
	shutdown := make(chan error, len(servers))
	for _, server := range servers {
		go func() { shutdown <- server.Shutdown(drain) }()
	}
	for range servers {
		if err := <-shutdown; err != nil {
			cancelRequests()
			for _, server := range servers {
				_ = server.Close()
			}
			result = errors.Join(result, fmt.Errorf("drain listeners: %w", err))
		}
	}
	// Shutdown does not track hijacked connections. Context cancellation also
	// stops gateway upgrade copies before its sampler/transports are closed.
	cancelRequests()
	admission.Lock()
	stopped = true
	admission.Unlock()
	handlers.Wait()
	return result
}

package main

// TODO: Add "-auto" flag to look for *-exporter processes and their port.
//       (and rescan the process list occasionally)

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"go.uber.org/zap"
	"tailscale.com/envknob"
	"tailscale.com/logtail"

	"github.com/jamessanford/tailmon/internal/log"
	"github.com/jamessanford/tailmon/internal/tshttp"
)

var usageMessage = `Usage:
    tailmon -state <dir> EXPORTER:PORT [EXPORTER:PORT ...]

Register one or more prometheus exporters on a tailscale network.  Requests to
port 80 on the tailnet will be proxied to a prometheus exporter on localhost.

For example, to register "node-exporter" and "postgres-exporter", run:

    tailmon -state /var/lib/tailmon node-exporter:9100 postgres-exporter:9187

Custom tailscale control servers may be set with TS_CONTROL_URL or --control-url

Flags:
`

func usage() {
	flag.CommandLine.Output().Write([]byte(usageMessage))
	flag.PrintDefaults()
	os.Exit(1)
}

func NewProxyHandler(logger *zap.Logger, upstreamURL *url.URL, name string) http.Handler {

	// NOTE: go1.20 introduces something new to replace Director.
	proxy := httputil.NewSingleHostReverseProxy(upstreamURL)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		// TODO: Add X-Forwarded-For header
		originalDirector(req)
	}
	stdlogger, err := zap.NewStdLogAt(logger.Named("proxy"), zap.ErrorLevel)
	if err == nil {
		proxy.ErrorLog = stdlogger
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			logger.Info("accept", zap.String("path", r.URL.Path))
			proxy.ServeHTTP(w, r)
		} else {
			logger.Info("reject", zap.String("path", r.URL.Path))
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, "%s\n", name)
		}
	})
}

func main() {
	flagDebug := flag.Bool("debug", false, "Print debug logs")
	flagState := flag.String("state", "", "Path to store tailnet state")
	flagNoLogs := flag.Bool("no-logs-no-support", true, "Disable logtail uploading")
	controlURL := flag.String("control-url", os.Getenv("TS_CONTROL_URL"), "URL of custom tailscale control server")
	flag.Usage = usage
	flag.Parse()

	var exporters []exporter
	for _, epStr := range flag.Args() {
		ep, err := newExporter(epStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %s\n", epStr, err)
			os.Exit(1)
		}
		exporters = append(exporters, ep)
	}

	if *flagState == "" {
		flag.CommandLine.Output().Write([]byte("ERROR: Must provide -state dir\n\n"))
		flag.Usage()
	}

	if len(exporters) == 0 {
		flag.CommandLine.Output().Write([]byte("ERROR: Must specify one or more exporters to announce.\n\n"))
		flag.Usage()
	}

	if *flagNoLogs {
		logtail.Disable()
		envknob.SetNoLogsNoSupport() // NOTE: This may not do anything?
	}

	rootLogger := log.MustZapLogger(*flagDebug)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var srvs []*tshttp.Server

	for _, ep := range exporters {
		logger := rootLogger.With(zap.String("name", ep.name))

		upstreamURL, err := url.Parse(fmt.Sprintf("http://%s:%d", "localhost", ep.port))
		if err != nil {
			logger.Fatal("unable to parse", zap.Error(err))
		}
		srv := &tshttp.Server{
			Logger:     logger,
			Name:       ep.TailscaleNodeName(),
			ControlURL: *controlURL,
			StateDir:   *flagState,
			Debug:      *flagDebug,
		}
		handler := NewProxyHandler(logger, upstreamURL, ep.TailscaleNodeName())
		if err := srv.Start(handler); err != nil {
			logger.Fatal("unable to initialize", zap.Error(err))
		}
		srvs = append(srvs, srv)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sigs:
	case <-ctx.Done():
	}

	var wg sync.WaitGroup
	for _, srv := range srvs {
		wg.Add(1)
		go func(srv *tshttp.Server) {
			srv.Shutdown()
			wg.Done()
		}(srv)
	}
	wg.Wait()
}

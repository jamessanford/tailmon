package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"go.uber.org/zap"
	"tailscale.com/envknob"
	"tailscale.com/logtail"
	"tailscale.com/tsnet"

	"github.com/jamessanford/tailmon/internal/log"
	"github.com/jamessanford/tailmon/internal/tshttp"
)

var usageMessage = `Usage:
    tailmon-discover -state <dir>

tailmon-disocver registers a node on a tailscale network, listens on port 80,
and returns a Prometheus HTTP SD response containing all "tailmon" nodes.

Run a single "tailmon-discover" along with many "tailmon" nodes to
automatically discover and monitor metrics endpoints over tailscale.

See example usage at https://github.com/jamessanford/tailmon/

Custom tailscale control servers may be set with TS_CONTROL_URL or --control-url

Flags:
`

func usage() {
	flag.CommandLine.Output().Write([]byte(usageMessage))
	flag.PrintDefaults()
	os.Exit(1)
}

type Endpoint struct {
	ip      netip.Addr        // for output sort
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

// formatAddr formats an IP address as a hostname (IPv6 gets brackets)
// There must be a better way to do this.
func formatAddr(addr netip.Addr, port int) string {
	switch {
	case addr.Is4():
		return fmt.Sprintf("%s:%d", addr.String(), port)
	default:
		return fmt.Sprintf("[%s]:%d", addr.String(), port)
	}
}

func findTailmonEndpoints(ctx context.Context, tailnet *tsnet.Server) ([]*Endpoint, error) {
	lc, err := tailnet.LocalClient()
	if err != nil {
		return nil, err
	}

	status, err := lc.Status(ctx)
	if err != nil {
		return nil, err
	}

	var endpoints []*Endpoint

	for _, v := range status.Peer {
		// NOTE: Ideally use Tags or Services to identify the
		// exporters, but that information is not present.
		// For now, use tailmon prefix.
		prefix := "tailmon/"
		if !strings.HasPrefix(v.HostName, prefix) {
			continue
		}
		if len(v.TailscaleIPs) == 0 {
			continue
		}

		exporter, node, ok := strings.Cut(strings.TrimPrefix(v.HostName, prefix), "/")
		if !ok {
			exporter = v.HostName
			node = "unknown"
		}

		// Prometheus scrapes all endpoints we provide,
		// so only provide one address per peer.
		endpoint := &Endpoint{
			ip:      v.TailscaleIPs[0], // for sorting
			Targets: []string{formatAddr(v.TailscaleIPs[0], 80)},
			Labels: map[string]string{
				"__meta_tailmon_node_name":     node,
				"__meta_tailmon_exporter_name": exporter,
				"__meta_tailscale_dns_name":    v.DNSName,
			},
		}
		endpoints = append(endpoints, endpoint)
	}

	sort.SliceStable(endpoints, func(i, j int) bool {
		return endpoints[i].ip.Less(endpoints[j].ip)
	})

	return endpoints, nil
}

func marshalEndpoints(ctx context.Context, tailnet *tsnet.Server) ([]byte, error) {
	endpoints, err := findTailmonEndpoints(ctx, tailnet)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(endpoints, "", "    ")
}

func NewDiscoverHandler(logger *zap.Logger, tailnet *tsnet.Server) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, "tailmon-discover\n")
			return
		}

		data, err := marshalEndpoints(r.Context(), tailnet)
		if err != nil {
			logger.Error("marshalEndpoints", zap.Error(err))
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, err.Error())
		}

		w.Header().Set("content-type", "application/json; charset=utf-8")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		// TODO: Add a /metrics endpoint to expose the entire tailnet.
		// (Use Status and WhoIs to export Hostinfo)
		return
	})
	return mux
}

func main() {
	flagDebug := flag.Bool("debug", false, "print debug logs")
	flagState := flag.String("state", "", "path to store tailnet state")
	flagNoLogs := flag.Bool("no-logs-no-support", true, "disable logtail uploading")
	controlURL := flag.String("control-url", os.Getenv("TS_CONTROL_URL"), "URL of custom tailscale control server")
	flag.Usage = usage
	flag.Parse()

	if flag.NArg() > 0 {
		flag.Usage()
	}

	if *flagState == "" {
		flag.CommandLine.Output().Write([]byte("ERROR: Must provide -state dir\n\n"))
		flag.Usage()
	}

	if *flagNoLogs {
		logtail.Disable()
		envknob.SetNoLogsNoSupport() // NOTE: This may not do anything.
	}

	logger := log.MustZapLogger(*flagDebug)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := &tshttp.Server{
		Logger:     logger,
		Name:       "tailmon-discover",
		ControlURL: *controlURL,
		StateDir:   *flagState,
		Debug:      *flagDebug,
	}
	tailnet := srv.Tailnet()
	handler := NewDiscoverHandler(logger, tailnet)
	if err := srv.Start(handler); err != nil {
		logger.Fatal("unable to initialize", zap.Error(err))
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sigs:
	case <-ctx.Done():
	}
	srv.Shutdown()
}

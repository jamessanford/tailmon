package tshttp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.uber.org/zap"
	"tailscale.com/tsnet"
	taillogger "tailscale.com/types/logger"
)

type Server struct {
	Logger     *zap.Logger
	Name       string
	ControlURL string
	StateDir   string
	Debug      bool
	tailnet    *tsnet.Server
	cancel     context.CancelFunc
	initOnce   sync.Once
}

func sanitize(path string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == rune('-') {
			return r
		}
		return '_'
	}, path)
}

func (s *Server) init() {
	if s.Logger == nil {
		s.Logger = zap.NewNop()
	}
	if s.Name == "" {
		s.Name = "unknown"
	}
	if s.StateDir == "" {
		s.StateDir = "."
	}
	s.Logger.Debug("tshttp init")

	var logf taillogger.Logf
	if s.Debug || s.Logger.Core().Enabled(zap.DebugLevel) {
		logf = s.Logger.Named("tsnet.Server").WithOptions(zap.AddCallerSkip(1)).Sugar().Infof
	} else {
		logf = taillogger.Discard
	}

	dir := fmt.Sprintf("%s/data-%s", s.StateDir, sanitize(s.Name))
	if err := os.MkdirAll(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		s.Logger.Fatal("unable to create state dir", zap.Error(err))
	}

	s.tailnet = &tsnet.Server{
		Dir:        dir,
		Hostname:   s.Name,
		ControlURL: s.ControlURL,
		Logf:       logf,
	}
}

// Tailnet returns the tsnet.Server which you might want access to
// before calling Start(handler) -- for example if your http handler uses
// the tailnet client.
func (s *Server) Tailnet() *tsnet.Server {
	s.initOnce.Do(s.init)
	return s.tailnet
}

// Start brings up the tailnet and starts serving HTTP on port 80.
// When authentication is needed to continue, a repeating log message
// will be output.  Use Shutdown when ready to stop HTTP and the tailnet.
func (s *Server) Start(handler http.Handler) error {
	s.initOnce.Do(s.init)

	logger := s.Logger

	logger.Info("tailnet starting")

	// Helper to show AuthURL when necessary.
	go func() {
		lc, err := s.tailnet.LocalClient()
		if err != nil {
			logger.Error("LocalClient", zap.Error(err))
			return
		}
		for ; ; time.Sleep(1 * time.Second) {
			// TODO: There should be a context to cancel to stop this goroutine.
			ss, err := lc.StatusWithoutPeers(context.Background())
			if err != nil {
				logger.Error("StatusWithoutPeers", zap.Error(err))
				continue
			}
			logger.Debug("status",
				zap.String("BackendState", ss.BackendState),
				zap.Strings("Health", ss.Health),
				zap.String("AuthURL", ss.AuthURL),
			)
			if ss.BackendState == "Running" {
				var ips []string
				for _, ip := range ss.TailscaleIPs {
					ips = append(ips, ip.String())
				}
				logger.Info("tailnet running",
					zap.String("id", fmt.Sprintf("%v", ss.Self.ID)),
					zap.String("dns", ss.Self.DNSName),
					zap.Strings("ips", ips),
				)
				// TODO: Instead of exiting, keep this goroutine around and log error events.
				break
			}
			if ss.AuthURL != "" {
				logger.Error("Needs authentication", zap.String("url", ss.AuthURL))
			}
		}
	}()

	logger.Debug("listen", zap.Int("port", 80))

	listen, err := s.tailnet.Listen("tcp", ":80")
	if err != nil {
		return err
	}

	httpsrv := &http.Server{Handler: handler} // TODO: Timeouts, ErrorLog, etc

	s.cancel = func() {
		httpctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		httpsrv.Shutdown(httpctx)
		cancel()
		listen.Close()
		s.tailnet.Close()
		logger.Info("shutdown")
	}

	go func() {
		logger.Debug("serving", zap.Int("port", 80))
		err = httpsrv.Serve(listen)
		switch {
		case err == nil:
			fallthrough
		case err == http.ErrServerClosed:
			logger.Info("shutting down")
		default:
			logger.Error("http.Serve", zap.Error(err))
		}
	}()

	return nil
}

// Shutdown is safe to call anytime after Start() has returned.
func (s *Server) Shutdown() {
	if s.cancel != nil {
		s.cancel()
	}
}

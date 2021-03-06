package corekit

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bmizerany/pat"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Service interface {
	Get(path string, handler APIHandler)
	Post(path string, handler APIHandler)
	Put(path string, handler APIHandler)
	Del(path string, handler APIHandler)
	Stream(path string, handler StreamAPIHandler)

	Run()
}

type ServeMux interface {
	Add(meth string, pat string, h http.Handler)
	ServeHTTP(w http.ResponseWriter, r *http.Request)
}

type Option func(o *Options)

type Options struct {
	name             string
	version          string
	dependenciesInfo map[string]func() interface{}
	params           map[string]string
	port             int
	certFile         string
	keyFile          string
	serveMux         ServeMux
	httpsEnabled     bool
	logger           func(format string, args ...interface{})
}

func Name(n string) Option {
	return func(o *Options) {
		o.name = n
	}
}

func Version(v string) Option {
	return func(o *Options) {
		o.version = v
	}
}

func DependencyInfo(name string, f func() interface{}) Option {
	return func(o *Options) {
		o.dependenciesInfo[name] = f
	}
}

func Param(name, val string) Option {
	return func(o *Options) {
		o.params[name] = val
	}
}

func Port(port int) Option {
	return func(o *Options) {
		o.port = port
	}
}

func Https(certFile, keyFile string) Option {
	return func(o *Options) {
		o.certFile = certFile
		o.keyFile = keyFile
		o.httpsEnabled = true
	}
}

func UseServeMux(mux ServeMux) Option {
	return func(o *Options) {
		o.serveMux = mux
	}
}

func Logger(l func(format string, args ...interface{})) Option {
	return func(o *Options) {
		o.logger = l
	}
}

func NewService(opts ...Option) Service {

	defaultLogger := log.New(os.Stdout, "", log.LUTC|log.LstdFlags|log.Lshortfile)

	options := &Options{
		dependenciesInfo: map[string]func() interface{}{},
		params:           map[string]string{},
		serveMux:         &adoptPatRouter{pat.New()},
		logger:           defaultLogger.Printf,
	}

	for _, o := range opts {
		o(options)
	}

	service := &service{
		options:          *options,
		wrapAPIHandler:   wrapAPIHandler(options.logger),
		streamAPIHandler: streamWrapAPIHandler(options.logger),
	}

	service.options.serveMux.Add(http.MethodGet, "/health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	service.options.serveMux.Add(http.MethodGet, "/info", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		dp := map[string]interface{}{}
		for name, d := range options.dependenciesInfo {
			dp[name] = d()
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"name":         options.name,
			"version":      options.version,
			"params":       options.params,
			"dependencies": dp,
		})
	}))

	service.options.serveMux.Add(http.MethodGet, "/metrics", promhttp.Handler())

	return service
}

type service struct {
	options          Options
	wrapAPIHandler   func(handler APIHandler) http.Handler
	streamAPIHandler func(handler StreamAPIHandler) http.Handler
}

func (s *service) Get(path string, handler APIHandler) {
	s.options.serveMux.Add(http.MethodGet, path, s.wrapAPIHandler(handler))
}

func (s *service) Post(path string, handler APIHandler) {
	s.options.serveMux.Add(http.MethodPost, path, s.wrapAPIHandler(handler))
}
func (s *service) Put(path string, handler APIHandler) {
	s.options.serveMux.Add(http.MethodPut, path, s.wrapAPIHandler(handler))
}
func (s *service) Del(path string, handler APIHandler) {
	s.options.serveMux.Add(http.MethodDelete, path, s.wrapAPIHandler(handler))
}

func (s *service) Stream(path string, handler StreamAPIHandler) {
	s.options.serveMux.Add(http.MethodGet, path, s.streamAPIHandler(handler))
}

func (s *service) Run() {
	s.options.logger("[INFO] Start listening address :%v\n", s.options.port)

	server := http.Server{
		Addr:    fmt.Sprint(":", s.options.port),
		Handler: s.options.serveMux,
	}

	ch := make(chan os.Signal)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		s.options.logger("[INFO] Graceful shutdown...\n")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			s.options.logger("[ERROR] %+v\n", err)
		}

		s.options.logger("[INFO] Service stoped\n")
	}()

	var err error
	if s.options.httpsEnabled {
		err = server.ListenAndServeTLS(s.options.certFile, s.options.keyFile)
	} else {
		err = server.ListenAndServe()
	}
	if err != nil && err != http.ErrServerClosed {
		s.options.logger("[ERROR] %+v\n", err)
	}
}

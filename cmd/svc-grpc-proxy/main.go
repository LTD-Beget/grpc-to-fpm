package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/soheilhy/cmux"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/transport"
	"gopkg.in/gemnasium/logrus-graylog-hook.v2"

	"github.com/LTD-Beget/grpc-to-fpm/internal/proxy"
)


var (
	gitHash                = "NO_HASH_DEFINED"
	ErrApplicationShutdown = errors.New("Application is shutting down")
)

func start() error {
	options, loadErr := proxy.LoadConfig()
	if loadErr != nil {
		return loadErr
	}

	debugSigChan := make(chan os.Signal, 10)
	signal.Notify(debugSigChan, syscall.SIGUSR2)
	defer func() {
		signal.Stop(debugSigChan)
		close(debugSigChan)
	}()
	go func() {
		for range debugSigChan {
			if logrus.GetLevel() == logrus.DebugLevel {
				logrus.SetLevel(logrus.InfoLevel)
				getLogger().Info("debug log is turned off")
			} else {
				logrus.SetLevel(logrus.DebugLevel)
				getLogger().Debug("debug log is turned on")
			}
		}
	}()

	if options.Graylog.Host != "" && options.Graylog.Port != "" {
		hook := graylog.NewAsyncGraylogHook(options.Graylog.Host+":"+options.Graylog.Port,
			map[string]interface{}{"platform": "svc-grpc-proxy", "instance_name": options.InstanceName})
		if hook.Writer() != nil {
			getLogger().WithFields(logrus.Fields{"platform": "svc-grpc-proxy", "instance_name": options.InstanceName}).Infof("graylog init complete")
		}
	}

	getLogger().Info("graylog destination is ", options.Graylog.Host, " ", options.Graylog.Port)
	getLogger().Info("instance name is ", options.InstanceName)

	if options.Debug {
		logrus.SetLevel(logrus.DebugLevel)
		getLogger().Debug("debug log is turned on")
	}

	var err error
	var lis net.Listener
	if options.CrtFile != "" && options.KeyFile != "" {
		getLogger().Infof("using TLS from (%s, %s)", options.CrtFile, options.KeyFile)

		cer, readErr := tls.LoadX509KeyPair(options.CrtFile, options.KeyFile)
		if readErr != nil {
			return errors.WithMessage(readErr, "failed to load key pair")
		}

		config := &tls.Config{
			ClientAuth:               tls.VerifyClientCertIfGiven,
			InsecureSkipVerify:       true,
			PreferServerCipherSuites: true,
			Certificates:             []tls.Certificate{cer},
			NextProtos:               []string{"h2"},
		}
		lis, err = tls.Listen("tcp", options.Host, config)
	} else {
		lis, err = net.Listen("tcp", options.Host)
	}

	if err != nil {
		return errors.WithMessage(err, fmt.Sprintf("failed to listen on %s", options.Host))
	}
	defer lis.Close()
	getLogger().WithField("host", options.Host).Info("binding successful")

	trafficMux := cmux.New(lis)
	//grpcL := trafficMux.MatchWithWriters(cmux.HTTP2MatchHeaderFieldSendSettings("content-type", "application/grpc"))
	grpcL := trafficMux.Match(cmux.HTTP2())
	httpL := trafficMux.Match(cmux.HTTP1Fast())

	group, ctx := errgroup.WithContext(context.Background())

	proxy := proxy.GRPCProxy{
		Logger: getLogger(),
		Handler: func(ctx context.Context, ts *transport.Stream, t transport.ServerTransport) {
			h := proxy.StreamHandler{
				Logger:        getLogger(),
				PortalOptions: options.Target,
			}
			h.HandleStream(ctx, ts, t)
		},
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		// Profiling endpoint
		getLogger().Println(http.ListenAndServe("localhost:6060", nil))
	}()

	group.Go(func() error {
		_, ok := <-sigCh
		grpcL.Close()
		if ok {
			go func() {
				<-sigCh
				os.Exit(-1)
			}()
		}
		return ErrApplicationShutdown
	})

	group.Go(func() error {
		getLogger().Info("starting serve")
		err = proxy.Serve(ctx, grpcL)
		if err != nil {
			return errors.WithMessage(err, "failed to serve")
		}
		return nil
	})

	// start http health check
	group.Go(func() error {
		getLogger().Info("starting http serve")

		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "This grpc entry point - please use grpc requests")
			w.WriteHeader(http.StatusMethodNotAllowed)
		})

		httpServer := &http.Server{Handler: mux}
		if err := httpServer.Serve(httpL); err != nil {
			return errors.WithMessage(err, "failed to serve http")
		}

		return nil
	})

	group.Go(func() error {
		getLogger().Info("starting tcp mux")
		if err := trafficMux.Serve(); err != nil {
			return errors.WithMessage(err, "failed to serve tcp mux")
		}

		return nil
	})

	return group.Wait()
}

// Test using http://localhost:8089/v1/test/wait?time=10
func main() {
	logger := getLogger()
	logrus.SetLevel(logrus.InfoLevel)
	logger.Info("proxy is starting up ", gitHash)
	err := start()
	if err != nil && errors.Cause(err) != ErrApplicationShutdown {
		logger.WithError(err).Error("application fail")
	} else {
		logger.Info("successful application shutdown. Goodbye!")
	}
}

func getLogger() logrus.FieldLogger {
	return logrus.StandardLogger()
}

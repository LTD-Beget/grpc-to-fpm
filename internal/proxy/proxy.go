package proxy

import (
	"net"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/transport"
)

type GRPCProxy struct {
	Logger  logrus.FieldLogger
	Handler func(ctx context.Context, ts *transport.Stream, t transport.ServerTransport)
}

func (gp *GRPCProxy) handleTransport(parentCtx context.Context, trans transport.ServerTransport) {
	transportCtx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	group, groupCtx := errgroup.WithContext(transportCtx)

	// Different context for FastCGI requests which we will cancel when we are done with current stream.
	cgiCtx, cgiCancel := context.WithCancel(context.Background())
	defer cgiCancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Wait for ctx cancellation, either by parentCtx or when function finished execution
		<-groupCtx.Done()

		// Stoping accepting new RPC's
		trans.Drain()

		wait := make(chan struct{})
		go func() {
			// Wait for handlers to complete their execution normally
			group.Wait()
			close(wait)
		}()

		select {
		// Only wait for 30 seconds, if not - force closing
		case <-time.After(30 * time.Second):
			cgiCancel()
		case <-wait:
		}

		// Wait for group anyway because force cancel is called
		<-wait

		// Terminate transport
		trans.Close()
	}()

	trans.HandleStreams(
		func(s *transport.Stream) {
			group.Go(func() error {
				gp.Handler(cgiCtx, s, trans)
				return nil
			})
		},
		func(ctx context.Context, method string) context.Context {
			group.Go(func() error {
				<-ctx.Done()
				return nil
			})
			return ctx
		},
	)

	// Cancel transport context, so we will shutdown transport in any way
	cancel()
	wg.Wait()

	gp.Logger.Debug("finishing our handler")
}

func (gp *GRPCProxy) Serve(ctx context.Context, lis net.Listener) error {
	group, _ := errgroup.WithContext(ctx)
	defer group.Wait()

	for {
		err := func() error {
			c, err := lis.Accept()
			if err != nil {
				return errors.WithMessage(err, "failed to Accept connection")
			}
			gp.Logger.WithField("remoteaddr", c.RemoteAddr()).Debug("accepted connection")

			config := &transport.ServerConfig{
				MaxStreams: 10000,
			}
			st, err := transport.NewServerTransport("http2", c, config)
			if err != nil {
				// Log transport err, but do not close the application
				gp.Logger.WithError(err).Error("failed to create grpc transport")
				c.Close()
				return nil
			}

			group.Go(func() error {
				gp.handleTransport(ctx, st)
				return nil
			})

			return nil
		}()

		if err != nil {
			return err
		}
	}
}

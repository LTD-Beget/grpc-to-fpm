package proxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/transport"
)

type StreamHandler struct {
	Logger        logrus.FieldLogger
	PortalOptions TargetOptions
}

func (s *StreamHandler) handleData(ctx context.Context, method, addr string, headers map[string][]string, data []byte) ([]byte, http.Header, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host = s.PortalOptions.ClientIP
		port = "9985"
	}

	forwardedFor := headers["x-forwarded-for"]
	if len(forwardedFor) > 0 {
		host = strings.Split(forwardedFor[0], ",")[0]
	}

	dstHost := s.PortalOptions.Host
	dstPort := s.PortalOptions.Port

	conf := Config{
		Host:       dstHost,
		Port:       dstPort,
		ScriptPath: s.PortalOptions.ScriptPath,
		ScriptName: s.PortalOptions.ScriptName,
		ClientIP:   host,
		ClientPort: port,
	}

	r := MakeRequest(conf)
	s.Logger.WithFields(logrus.Fields{
		"method":     method,
		"dsthost":    dstHost,
		"dstport":    dstPort,
		"scriptpath": conf.ScriptPath,
		"scriptname": conf.ScriptName,
		"datalen":    len(data),
		"headers":    headers,
		"data":       bstringer(data),
	}).Debug("making a request")

	innerCtx := context.WithValue(ctx, "logruslogger", s.Logger)
	// Three attempts to make request, because php can return error in first two ones.
	// Also our fcgi client may had run out of id's
	i := 0
	for {
		bdata, resHeaders, err := r.Do(innerCtx, method, headers, bytes.NewReader(data), i < 2)
		if err != nil {
			if i < 3 {
				i++
				continue
			}
			return nil, nil, errors.WithMessage(err, "failed to make FCGI request")
		}

		s.Logger.WithFields(logrus.Fields{
			"method":     method,
			"dsthost":    dstHost,
			"dstport":    dstPort,
			"scriptpath": conf.ScriptPath,
			"scriptname": conf.ScriptName,
			"resHeaders": resHeaders,
			"data":       bstringer(bdata),
		}).Debug("completed request")
		return bdata, resHeaders, nil
	}
}

func (s *StreamHandler) recvMsg(r io.Reader) (msg []byte, err error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, errors.WithMessage(err, "failed to read grpc header")
	}

	typ := header[0]
	length := binary.BigEndian.Uint32(header[1:])
	s.Logger.WithFields(logrus.Fields{
		"type":   typ,
		"length": length,
	}).Debug("got msg header")

	if length == 0 {
		return nil, nil
	}

	msg = make([]byte, int(length))
	if _, err := io.ReadFull(r, msg); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, errors.WithMessage(err, "failed to read message body")
	}
	s.Logger.Debug("got message body")
	return msg, nil
}

func copyHeaders(md metadata.MD, _ bool) map[string][]string {
	// Add 10 just in case we will need to add additional headers
	result := make(map[string][]string, len(md)+10)
	for k, v := range md {
		result[k] = v
	}

	return result
}

func (s *StreamHandler) HandleStream(ctx context.Context, ts *transport.Stream, t transport.ServerTransport) {
	headers := copyHeaders(metadata.FromIncomingContext(ts.Context()))

	requestId := getRequestId()
	s.Logger = s.Logger.WithField("request_id", requestId)
	// TODO: maybe we should move logging into context
	if len(headers["global_request_id"]) != 0 {
		s.Logger = s.Logger.WithField("global_request_id", headers["global_request_id"][0])
	} else {
		s.Logger = s.Logger.WithField("global_request_id", requestId)
		headers["global_request_id"] = []string{requestId}
	}

	remoteAddr := t.RemoteAddr().String()

	data, err := s.recvMsg(ts)
	if err != nil {
		s.Logger.WithError(err).Error("failed to receive a message")
		t.WriteStatus(ts, status.New(codes.Unknown, "Failed to parse message"))
		return
	}
	requestBegin := time.Now()

	returnData, resHeaders, err := s.handleData(ctx, ts.Method(), remoteAddr, headers, data)
	if err != nil {
		s.Logger.WithError(err).Error("failed to handle a message")
		t.WriteStatus(ts, status.New(codes.Unavailable, "Failed to send a message"))
		return
	}

	if resHeaders["X-Grpc-Status"] != nil && len(resHeaders["X-Grpc-Status"]) > 0 && resHeaders["X-Grpc-Status"][0] == "ERROR" {
		s.Logger.WithField("X-Grpc-Error-Description", resHeaders["X-Grpc-Error-Description"][0]).Error("Fast CGI Server has returned an error")
		errorCode, err := getErrorCode(resHeaders)
		if err != nil {
			s.Logger.WithError(err).Error("Couldn't convert X-Grpc-Error-Code, using internal code")
		}
		if s.PortalOptions.ReturnError {
			t.WriteStatus(ts, status.New(errorCode, getErrorDescription(errorCode, resHeaders)))
		} else {
			t.WriteStatus(ts, status.New(errorCode, errorCode.String()))
		}

		return
	}

	var header [5]byte
	s.Logger.WithFields(logrus.Fields{
		"type":   header[0],
		"length": len(returnData),
		"time":   time.Since(requestBegin),
	}).Debug("returning result from FCGI")
	binary.BigEndian.PutUint32(header[1:], uint32(len(returnData)))

	opts := &transport.Options{
		Last:  true,
		Delay: false,
	}

	err = t.Write(ts, header[:], returnData, opts)
	s.Logger.WithError(err).Debug("write completed")
	if err != nil {
		switch err.(type) {
		case transport.ConnectionError:
			//TODO: Handle this
		case transport.StreamError:
			//TODO: Handle this
		default:
			//TODO: Handle this
		}
		t.WriteStatus(ts, status.New(codes.Unknown, "Failed to send message back"))
		return
	}
	t.WriteStatus(ts, status.New(codes.OK, ""))
}

func getRequestId() string {
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	micro := time.Now().Truncate(time.Microsecond).UnixNano()
	return strconv.FormatInt(micro, 10) + `.` + strconv.Itoa(rnd.Intn(500))
}

func getErrorCode(hr http.Header) (codes.Code, error) {
	if hr["X-Grpc-Error-Code"] == nil {
		return codes.Unknown, nil
	}

	code, err := strconv.Atoi(hr["X-Grpc-Error-Code"][0])
	if err != nil {
		return codes.Internal, err
	}

	return codes.Code(code), nil
}

func getErrorDescription(errorCode codes.Code, hr http.Header) string {
	switch {
	case errorCode == codes.Internal:
		return "Internal service error"
	case len(hr["X-Grpc-Error-Description"]) == 0:
		return "Description of error has not been transfered"
	default:
		return hr["X-Grpc-Error-Description"][0]
	}
}

type bstringer []byte

func (b bstringer) String() string {
	return string(b)
}

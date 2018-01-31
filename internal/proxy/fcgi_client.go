package proxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/textproto"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/kellegous/fcgi"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

var (
	regtransform = regexp.MustCompile("([^A-Z])?([A-Z])")
)

type SizedReader interface {
	io.Reader
	Size() int64
}

type Config struct {
	Host       string
	Port       int
	ScriptPath string
	ScriptName string
	ClientIP   string
	ClientPort string
}

type Request struct {
	config Config
}

func MakeRequest(config Config) Request {
	return Request{
		config: config,
	}
}

// TODO: Add connection pooling even if we use connection only once
func (r *Request) Do(ctx context.Context, request string, headers map[string][]string, sr SizedReader, usePool bool) ([]byte, http.Header, error) {
	destination := fmt.Sprintf("%s:%d", r.config.Host, r.config.Port)
	//logrus.WithField("destination", destination).Infof("creating a new fcgi connection")
	client, err := fcgi.NewClient("tcp", destination)
	if err != nil {
		return nil, nil, err
	}

	convertedRequest, err := r.convert(request)
	if err != nil {
		return nil, nil, err
	}

	requestParams := r.getCgiParams(map[string]string{
		"r": convertedRequest,
	}, int(sr.Size()))

	// Add passed headers with custom prefix.
	for key, val := range headers {
		requestParams["X-GRPC-"+key] = val
	}

	logger, ok := ctx.Value("logruslogger").(logrus.FieldLogger)
	if !ok {
		logger = logrus.StandardLogger()
		logger.Warn("Failed to get logger from ctx")
	}

	wout := &bytes.Buffer{}
	werr := &bytes.Buffer{}

	req, err := client.NewRequest(requestParams)
	if err != nil {
		return nil, nil, errors.WithMessage(err, "failed to begin FCGI request")
	}

	req.Stdin = sr
	req.Stdout = wout
	req.Stderr = werr

	if err = req.Wait(); err != nil {
		return nil, nil, errors.WithMessage(err, "failed to wait for FCGI response")
	}

	rb := bufio.NewReader(wout)
	tp := textproto.NewReader(rb)

	// Parse the response headers.
	mimeHeader, err := tp.ReadMIMEHeader()
	if err != nil {
		return nil, nil, errors.WithMessage(err, "failed to parse FCGI response header")
	}
	header := http.Header(mimeHeader)

	// TODO: fixTransferEncoding ?
	transferEncoding := header["Transfer-Encoding"]
	//contentLength, _ := strconv.ParseInt(header.Get("Content-Length"), 10, 64)

	var result []byte

	if chunked(transferEncoding) {
		result, err = ioutil.ReadAll(httputil.NewChunkedReader(rb))
		if err != nil {
			return nil, nil, errors.WithMessage(err, "failed to parse FCGI chunked response")
		}
	} else {
		result, err = ioutil.ReadAll(rb)
		if err != nil {
			return nil, nil, errors.WithMessage(err, "failed to parse FCGI response")
		}
	}

	return result, header, nil
}

func chunked(te []string) bool { return len(te) > 0 && te[0] == "chunked" }

func (r *Request) getCgiParams(params map[string]string, length int) map[string][]string {
	path := r.config.ScriptPath + "/" + r.config.ScriptName
	scriptName := "/" + r.config.ScriptName

	return map[string][]string{
		"GATEWAY_INTERFACE": {"FastCGI/1.0"},
		"REQUEST_METHOD":    {"POST"},
		"SCRIPT_FILENAME":   {path},
		"SCRIPT_NAME":       {path},
		"QUERY_STRING":      {r.urlEncode(params)},
		"REQUEST_URI":       {scriptName},
		"DOCUMENT_URI":      {scriptName},
		"SERVER_SOFTWARE":   {"php/fcgiclient"},
		"REMOTE_ADDR":       {r.config.ClientIP},
		"REMOTE_PORT":       {r.config.ClientPort},
		"SERVER_ADDR":       {r.config.Host},
		"SERVER_PORT":       {strconv.Itoa(r.config.Port)},
		"SERVER_NAME":       {"golang-grpc-proxy/1.1"},
		"SERVER_PROTOCOL":   {"HTTP/1.1"},
		"CONTENT_TYPE":      {"application/octet-stream"},
		"CONTENT_LENGTH":    {strconv.Itoa(length)},
	}
}

func (r *Request) convert(request string) (string, error) {
	return strings.ToLower(regtransform.ReplaceAllString(request, "${1}-$2")), nil
}

func (r *Request) urlEncode(params map[string]string) string {
	vals := make(url.Values, len(params))
	for k, v := range params {
		vals.Set(k, v)
	}
	return vals.Encode()
}

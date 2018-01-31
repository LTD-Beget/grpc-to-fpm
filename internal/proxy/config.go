package proxy

import (
	"fmt"

	"github.com/jinzhu/configor"
	"github.com/pkg/errors"
)

type TargetOptions struct {
	Host        string
	Port        int
	Name        string
	ScriptPath  string `required:"true"`
	ScriptName  string `required:"true"`
	ClientIP    string `required:"true"`
	ReturnError bool
}

type GraylogOptions struct {
	Host string
	Port string
}

type Options struct {
	Debug        bool   `default:"false"`
	Host         string `required:"true"`
	KeyFile      string
	CrtFile      string
	InstanceName string `required:"true"`
	Target       TargetOptions
	Graylog      GraylogOptions
}

func LoadConfig() (*Options, error) {
	options := &Options{}
	err := configor.Load(options, "grpc-proxy-config.yml")
	if err != nil {
		return nil, errors.WithMessage(err, "failed to load configuration")
	}

	fmt.Printf("OPTIONS: %#v\n", options)
	return options, nil
}
